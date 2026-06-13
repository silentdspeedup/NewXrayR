// Package accesslog captures xray-core access log messages in-process and
// reports them to the panel in batches, per node. It wraps (not replaces) the
// core's app/log handler so local access/error file logging keeps working.
package accesslog

import (
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"

	applog "github.com/xtls/xray-core/app/log"
	xlog "github.com/xtls/xray-core/common/log"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"

	"github.com/XrayR-project/XrayR/api"
)

// routed is an entry plus its routing tag (empty for untagged probe rows).
type routed struct {
	entry api.AccessLogEntry
	tag   string
}

// target is a single node's reporting destination + buffer.
type target struct {
	reporter api.AccessLogReporter
	nodeID   int
	mu       sync.Mutex
	buf      []api.AccessLogEntry
	notify   chan struct{}
}

// Manager consumes captured access log entries, routes them to per-node
// buffers by inbound tag, and flushes each buffer to its panel in batches.
type Manager struct {
	cfg     *Config
	server  *core.Instance
	logger  *log.Entry
	ingest  chan routed
	routes  map[string]*target // inboundTag -> target
	all     []*target          // all registered targets (for broadcast)
	routeMu sync.RWMutex
	dropped uint64
	done    chan struct{}
}

// New creates a Manager. server is used to fetch the app/log handler to wrap.
func New(cfg *Config, server *core.Instance, logger *log.Entry) *Manager {
	cfg.normalize()
	return &Manager{
		cfg:    cfg,
		server: server,
		logger: logger,
		ingest: make(chan routed, cfg.IngestChanSize),
		routes: make(map[string]*target),
		done:   make(chan struct{}),
	}
}

// Register adds a node's reporting target keyed by its inbound tag. Called by
// each controller during Start, before Manager.Start installs the handler.
func (m *Manager) Register(tag string, reporter api.AccessLogReporter, nodeID int) {
	m.routeMu.Lock()
	defer m.routeMu.Unlock()
	if _, exists := m.routes[tag]; exists {
		return
	}
	t := &target{reporter: reporter, nodeID: nodeID, notify: make(chan struct{}, 1)}
	m.routes[tag] = t
	m.all = append(m.all, t)
}

// Start installs the wrapping log handler and launches consumer/flusher goroutines.
func (m *Manager) Start() {
	// Fetch the core's app/log instance to delegate to (preserves local files).
	var next xlog.Handler
	if f := m.server.GetFeature((*applog.Instance)(nil)); f != nil {
		if h, ok := f.(xlog.Handler); ok {
			next = h
		}
	}
	if next == nil {
		m.logger.Warn("access log: app/log handler not found; local access/error files may not be written")
	}
	xlog.RegisterHandler(&xrayLogHandler{next: next, mgr: m})

	m.routeMu.RLock()
	targets := append([]*target(nil), m.all...)
	m.routeMu.RUnlock()

	go m.consume()
	go m.statsLoop()
	for _, t := range targets {
		go m.flush(t)
	}
	m.logger.Printf("access log reporter started: %d node(s), probe policy=%s", len(targets), m.cfg.ProbeReportPolicy)
}

// Close stops the manager. The wrapper handler stays installed (process exiting).
func (m *Manager) Close() {
	select {
	case <-m.done:
	default:
		close(m.done)
	}
}

// consume routes captured entries from the ingest channel to node buffers.
func (m *Manager) consume() {
	for {
		select {
		case <-m.done:
			return
		case r := <-m.ingest:
			m.dispatch(r)
		}
	}
}

func (m *Manager) dispatch(r routed) {
	if r.tag != "" {
		m.routeMu.RLock()
		t := m.routes[r.tag]
		m.routeMu.RUnlock()
		if t != nil {
			m.enqueue(t, r.entry)
		} else {
			atomic.AddUint64(&m.dropped, 1) // tag of an unregistered/disabled node
		}
		return
	}
	// Untagged probe row: route per policy.
	switch m.cfg.ProbeReportPolicy {
	case ProbeNone:
		return
	case ProbeFirst:
		m.routeMu.RLock()
		var t *target
		if len(m.all) > 0 {
			t = m.all[0]
		}
		m.routeMu.RUnlock()
		if t != nil {
			m.enqueue(t, r.entry)
		}
	default: // broadcast
		m.routeMu.RLock()
		all := append([]*target(nil), m.all...)
		m.routeMu.RUnlock()
		for _, t := range all {
			m.enqueue(t, r.entry)
		}
	}
}

// enqueue appends an entry to a node buffer, dropping the oldest if full.
func (m *Manager) enqueue(t *target, e api.AccessLogEntry) {
	t.mu.Lock()
	if len(t.buf) >= m.cfg.BufferMax {
		t.buf = t.buf[1:] // drop oldest
		atomic.AddUint64(&m.dropped, 1)
	}
	t.buf = append(t.buf, e)
	reached := len(t.buf) >= m.cfg.BatchSize
	t.mu.Unlock()
	if reached {
		select {
		case t.notify <- struct{}{}:
		default:
		}
	}
}

// flush drains a node buffer on batch-size signal or flush interval.
func (m *Manager) flush(t *target) {
	ticker := time.NewTicker(m.cfg.flushInterval())
	defer ticker.Stop()
	for {
		select {
		case <-m.done:
			m.flushOnce(t) // best-effort final drain
			return
		case <-ticker.C:
			m.flushOnce(t)
		case <-t.notify:
			m.flushOnce(t)
		}
	}
}

// flushOnce sends all buffered entries for a target in MaxBatch-sized POSTs.
func (m *Manager) flushOnce(t *target) {
	for {
		t.mu.Lock()
		n := len(t.buf)
		if n == 0 {
			t.mu.Unlock()
			return
		}
		if n > m.cfg.MaxBatch {
			n = m.cfg.MaxBatch
		}
		batch := make([]api.AccessLogEntry, n)
		copy(batch, t.buf[:n])
		t.buf = append(t.buf[:0], t.buf[n:]...) // compact remainder to front
		t.mu.Unlock()

		m.report(t, batch)
	}
}

// report POSTs a batch with exponential backoff. A persistent failure (network
// outage or ret=0 misconfig) is dropped after maxAttempts to avoid an infinite
// loop; new entries keep flowing into the bounded buffer.
func (m *Manager) report(t *target, batch []api.AccessLogEntry) {
	const maxAttempts = 6
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for attempt := 1; ; attempt++ {
		if err := t.reporter.ReportAccessLog(&batch); err == nil {
			return
		} else if attempt >= maxAttempts {
			m.logger.Warnf("access log: node %d giving up on %d entries after %d attempts: %s",
				t.nodeID, len(batch), attempt, err)
			atomic.AddUint64(&m.dropped, uint64(len(batch)))
			return
		} else {
			m.logger.Warnf("access log: node %d report failed (%d entries), retry %d in %s: %s",
				t.nodeID, len(batch), attempt, backoff, err)
		}
		select {
		case <-m.done:
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// statsLoop periodically logs the dropped counter when it grows.
func (m *Manager) statsLoop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	var last uint64
	for {
		select {
		case <-m.done:
			return
		case <-ticker.C:
			if cur := atomic.LoadUint64(&m.dropped); cur > last {
				m.logger.Warnf("access log: dropped %d entries in the last minute (total %d)", cur-last, cur)
				last = cur
			}
		}
	}
}

// xrayLogHandler wraps the core app/log handler: it forwards every message to
// the delegate (so local files keep being written) and additionally captures
// AccessMessages for reporting. Handle runs inline on the proxy hot path, so it
// must stay non-blocking.
type xrayLogHandler struct {
	next xlog.Handler
	mgr  *Manager
}

func (h *xrayLogHandler) Handle(msg xlog.Message) {
	if h.next != nil {
		h.next.Handle(msg) // local access/error file logging, unchanged
	}
	am, ok := msg.(*xlog.AccessMessage)
	if !ok {
		return
	}
	// Probe rows (no user) when reporting of invalid users is disabled: skip.
	if am.Email == "" && !h.mgr.cfg.reportInvalidUser() {
		return
	}
	e, tag := buildEntry(am)
	select {
	case h.mgr.ingest <- routed{entry: e, tag: tag}:
	default: // channel full -> drop, never block the proxy
		atomic.AddUint64(&h.mgr.dropped, 1)
	}
}

// buildEntry extracts a structured AccessLogEntry and the routing tag from a
// raw AccessMessage. Returns tag == "" for untagged probe rows.
func buildEntry(am *xlog.AccessMessage) (api.AccessLogEntry, string) {
	srcIP, srcPort := extractSrc(am.From)
	e := api.AccessLogEntry{
		Ts:     time.Now().UTC().Unix(),
		SrcIP:  srcIP,
		Action: string(am.Status),
	}
	if srcPort > 0 {
		e.SrcPort = &srcPort
	}
	setDest(&e, am.To)
	if am.Status == xlog.AccessRejected && e.DestHost == nil {
		if r := serial.ToString(am.Reason); r != "" {
			r = truncate(r, 64)
			e.RejectReason = &r
		}
	}
	if am.Email != "" {
		tag, mail, uid := splitUserTag(am.Email)
		if mail != "" {
			e.Email = &mail
		}
		if n, err := strconv.Atoi(uid); err == nil {
			e.UserID = &n
		}
		return e, tag
	}
	return e, ""
}

// setDest fills network/host/port from the AccessMessage To field.
func setDest(e *api.AccessLogEntry, to interface{}) {
	if to == nil {
		return
	}
	if d, ok := to.(xnet.Destination); ok {
		if d.Network == xnet.Network_Unknown || d.Address == nil {
			return
		}
		nw := strings.ToLower(d.Network.String())
		host := unbracket(d.Address.String())
		port := int(d.Port.Value())
		e.Network, e.DestHost, e.DestPort = &nw, &host, &port
		return
	}
	// Fallback: parse "tcp:host:port" string form.
	s := serial.ToString(to)
	i := strings.Index(s, ":")
	j := strings.LastIndex(s, ":")
	if i <= 0 || j <= i {
		return
	}
	nw := strings.ToLower(s[:i])
	host := unbracket(s[i+1 : j])
	if p, err := strconv.Atoi(s[j+1:]); err == nil {
		e.Network, e.DestHost, e.DestPort = &nw, &host, &p
	}
}

// extractSrc returns the bare source IP (no port, no brackets) and the source
// port from an AccessMessage From field, which may be a net.Destination, a
// net.Addr string, or a "tcp:ip:port" string. port is -1 when not present.
func extractSrc(v interface{}) (ip string, port int) {
	port = -1
	if d, ok := v.(xnet.Destination); ok {
		if d.Address != nil {
			ip = unbracket(d.Address.String())
		}
		if d.Port != 0 {
			port = int(d.Port.Value())
		}
		return ip, port
	}
	s := stripNetworkPrefix(serial.ToString(v))
	if host, p, err := net.SplitHostPort(s); err == nil {
		if n, e := strconv.Atoi(p); e == nil {
			port = n
		}
		return host, port
	}
	return unbracket(s), port
}

// stripNetworkPrefix removes a leading "tcp:"/"udp:"/"unix:"/"unknown:" prefix.
func stripNetworkPrefix(s string) string {
	if i := strings.Index(s, ":"); i >= 0 {
		switch s[:i] {
		case "tcp", "udp", "unix", "unknown":
			return s[i+1:]
		}
	}
	return s
}

func unbracket(s string) string {
	return strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
}

// splitUserTag splits an xray user email "tag|email|uid" by the first and last
// '|'. Returns empty strings if it is not the expected 3-part form.
func splitUserTag(s string) (tag, email, uid string) {
	first := strings.Index(s, "|")
	last := strings.LastIndex(s, "|")
	if first < 0 || last == first {
		return "", "", ""
	}
	return s[:first], s[first+1 : last], s[last+1:]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

package accesslog

import (
	"errors"
	"net"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
	xlog "github.com/xtls/xray-core/common/log"
	xnet "github.com/xtls/xray-core/common/net"

	"github.com/XrayR-project/XrayR/api"
)

func strOr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func intOr(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}

func TestBuildEntry_AcceptedIPv4Domain(t *testing.T) {
	am := &xlog.AccessMessage{
		From:   xnet.TCPDestination(xnet.ParseAddress("223.91.85.118"), 50410),
		To:     xnet.TCPDestination(xnet.ParseAddress("cp.cloudflare.com"), 80),
		Status: xlog.AccessAccepted,
		Email:  "V2ray_0.0.0.0_16602|jodakkkda124@163.com|38308",
	}
	e, tag := buildEntry(am)

	if tag != "V2ray_0.0.0.0_16602" {
		t.Errorf("tag = %q", tag)
	}
	if e.SrcIP != "223.91.85.118" {
		t.Errorf("SrcIP = %q", e.SrcIP)
	}
	if e.Action != "accepted" {
		t.Errorf("Action = %q", e.Action)
	}
	if strOr(e.Network) != "tcp" {
		t.Errorf("Network = %q", strOr(e.Network))
	}
	if strOr(e.DestHost) != "cp.cloudflare.com" {
		t.Errorf("DestHost = %q", strOr(e.DestHost))
	}
	if intOr(e.DestPort) != 80 {
		t.Errorf("DestPort = %d", intOr(e.DestPort))
	}
	if strOr(e.Email) != "jodakkkda124@163.com" {
		t.Errorf("Email = %q", strOr(e.Email))
	}
	if intOr(e.UserID) != 38308 {
		t.Errorf("UserID = %d", intOr(e.UserID))
	}
	if e.RejectReason != nil {
		t.Errorf("RejectReason = %q, want nil", *e.RejectReason)
	}
	if e.Ts <= 0 {
		t.Errorf("Ts = %d", e.Ts)
	}
}

func TestBuildEntry_AcceptedIPv6UDP(t *testing.T) {
	src := "2406:280:1005:f134:184:8f81:7985:546b"
	am := &xlog.AccessMessage{
		From:   xnet.UDPDestination(xnet.ParseAddress(src), 6287),
		To:     xnet.UDPDestination(xnet.ParseAddress("138.113.154.38"), 443),
		Status: xlog.AccessAccepted,
		Email:  "tag|376197120@qq.com|43450",
	}
	e, _ := buildEntry(am)

	wantSrc := net.ParseIP(src).String() // canonical form, no brackets
	if e.SrcIP != wantSrc {
		t.Errorf("SrcIP = %q, want %q", e.SrcIP, wantSrc)
	}
	if strings.ContainsAny(e.SrcIP, "[]") {
		t.Errorf("SrcIP contains brackets: %q", e.SrcIP)
	}
	if strOr(e.Network) != "udp" {
		t.Errorf("Network = %q", strOr(e.Network))
	}
	if strOr(e.DestHost) != "138.113.154.38" {
		t.Errorf("DestHost = %q", strOr(e.DestHost))
	}
	if intOr(e.DestPort) != 443 {
		t.Errorf("DestPort = %d", intOr(e.DestPort))
	}
}

func TestBuildEntry_RejectedWithUID(t *testing.T) {
	am := &xlog.AccessMessage{
		From:   xnet.TCPDestination(xnet.ParseAddress("1.2.3.4"), 1111),
		To:     xnet.TCPDestination(xnet.ParseAddress("blocked.example.com"), 443),
		Status: xlog.AccessRejected,
		Email:  "tag|user@x.com|123",
	}
	e, tag := buildEntry(am)

	if tag != "tag" {
		t.Errorf("tag = %q", tag)
	}
	if e.Action != "rejected" {
		t.Errorf("Action = %q", e.Action)
	}
	if intOr(e.UserID) != 123 {
		t.Errorf("UserID = %d", intOr(e.UserID))
	}
	if strOr(e.Email) != "user@x.com" {
		t.Errorf("Email = %q", strOr(e.Email))
	}
	// has a target tuple -> no reject_reason
	if e.RejectReason != nil {
		t.Errorf("RejectReason = %q, want nil", *e.RejectReason)
	}
}

func TestBuildEntry_RejectedNoUID_Probe(t *testing.T) {
	longReason := strings.Repeat("X", 100)
	am := &xlog.AccessMessage{
		From:   &net.TCPAddr{IP: net.ParseIP("111.32.121.209"), Port: 6676},
		To:     "", // no destination tuple
		Status: xlog.AccessRejected,
		Reason: errors.New(longReason),
		Email:  "", // probe / invalid user
	}
	e, tag := buildEntry(am)

	if tag != "" {
		t.Errorf("tag = %q, want empty", tag)
	}
	if e.SrcIP != "111.32.121.209" {
		t.Errorf("SrcIP = %q", e.SrcIP)
	}
	if e.UserID != nil || e.Email != nil {
		t.Errorf("UserID/Email should be nil for probe row")
	}
	if e.Network != nil || e.DestHost != nil || e.DestPort != nil {
		t.Errorf("dest fields should be nil for probe row")
	}
	if e.RejectReason == nil {
		t.Fatalf("RejectReason should be set")
	}
	if len(*e.RejectReason) != 64 {
		t.Errorf("RejectReason len = %d, want 64 (truncated)", len(*e.RejectReason))
	}
}

func TestSplitUserTag(t *testing.T) {
	cases := []struct {
		in              string
		tag, email, uid string
	}{
		{"V2ray_0.0.0.0_16602|jodakkkda124@163.com|38308", "V2ray_0.0.0.0_16602", "jodakkkda124@163.com", "38308"},
		{"tag|weird+name.x@sub.example.co|999", "tag", "weird+name.x@sub.example.co", "999"},
		{"noemail", "", "", ""},
		{"only|onepipe", "", "", ""},
		{"", "", "", ""},
	}
	for _, c := range cases {
		tag, email, uid := splitUserTag(c.in)
		if tag != c.tag || email != c.email || uid != c.uid {
			t.Errorf("splitUserTag(%q) = (%q,%q,%q), want (%q,%q,%q)",
				c.in, tag, email, uid, c.tag, c.email, c.uid)
		}
	}
}

// fakeReporter records nothing; routing tests inspect target buffers directly.
type fakeReporter struct{}

func (fakeReporter) ReportAccessLog(_ *[]api.AccessLogEntry) error { return nil }

func newTestManager(policy string) *Manager {
	cfg := &Config{Enable: true, ProbeReportPolicy: policy, BufferMax: 5, BatchSize: 1000}
	return New(cfg, nil, log.NewEntry(log.StandardLogger()))
}

func bufLen(m *Manager, tag string) int {
	t := m.routes[tag]
	if t == nil {
		return -1
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.buf)
}

func TestDispatch_RouteByTag(t *testing.T) {
	m := newTestManager(ProbeBroadcast)
	m.Register("tagA", fakeReporter{}, 12)
	m.Register("tagB", fakeReporter{}, 34)

	m.dispatch(routed{entry: api.AccessLogEntry{}, tag: "tagA"})
	m.dispatch(routed{entry: api.AccessLogEntry{}, tag: "tagA"})
	m.dispatch(routed{entry: api.AccessLogEntry{}, tag: "tagB"})

	if got := bufLen(m, "tagA"); got != 2 {
		t.Errorf("tagA buf = %d, want 2", got)
	}
	if got := bufLen(m, "tagB"); got != 1 {
		t.Errorf("tagB buf = %d, want 1", got)
	}
}

func TestDispatch_UnknownTagDropped(t *testing.T) {
	m := newTestManager(ProbeBroadcast)
	m.Register("tagA", fakeReporter{}, 12)
	m.dispatch(routed{entry: api.AccessLogEntry{}, tag: "ghost"})
	if got := bufLen(m, "tagA"); got != 0 {
		t.Errorf("tagA buf = %d, want 0", got)
	}
	if m.dropped != 1 {
		t.Errorf("dropped = %d, want 1", m.dropped)
	}
}

func TestDispatch_ProbeBroadcast(t *testing.T) {
	m := newTestManager(ProbeBroadcast)
	m.Register("tagA", fakeReporter{}, 12)
	m.Register("tagB", fakeReporter{}, 34)
	m.dispatch(routed{entry: api.AccessLogEntry{}, tag: ""}) // probe row
	if bufLen(m, "tagA") != 1 || bufLen(m, "tagB") != 1 {
		t.Errorf("broadcast: tagA=%d tagB=%d, want 1/1", bufLen(m, "tagA"), bufLen(m, "tagB"))
	}
}

func TestDispatch_ProbeFirst(t *testing.T) {
	m := newTestManager(ProbeFirst)
	m.Register("tagA", fakeReporter{}, 12)
	m.Register("tagB", fakeReporter{}, 34)
	m.dispatch(routed{entry: api.AccessLogEntry{}, tag: ""})
	if bufLen(m, "tagA") != 1 || bufLen(m, "tagB") != 0 {
		t.Errorf("first: tagA=%d tagB=%d, want 1/0", bufLen(m, "tagA"), bufLen(m, "tagB"))
	}
}

func TestDispatch_ProbeNone(t *testing.T) {
	m := newTestManager(ProbeNone)
	m.Register("tagA", fakeReporter{}, 12)
	m.dispatch(routed{entry: api.AccessLogEntry{}, tag: ""})
	if bufLen(m, "tagA") != 0 {
		t.Errorf("none: tagA=%d, want 0", bufLen(m, "tagA"))
	}
}

func TestEnqueue_DropOldestWhenFull(t *testing.T) {
	m := newTestManager(ProbeBroadcast) // BufferMax = 5
	m.Register("tagA", fakeReporter{}, 12)
	for i := 0; i < 8; i++ {
		m.dispatch(routed{entry: api.AccessLogEntry{Ts: int64(i)}, tag: "tagA"})
	}
	if got := bufLen(m, "tagA"); got != 5 {
		t.Fatalf("buf = %d, want 5 (capped)", got)
	}
	// oldest (Ts 0,1,2) dropped; buffer should hold Ts 3..7
	tt := m.routes["tagA"]
	tt.mu.Lock()
	first := tt.buf[0].Ts
	tt.mu.Unlock()
	if first != 3 {
		t.Errorf("oldest Ts = %d, want 3", first)
	}
}

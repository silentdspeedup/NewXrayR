package accesslog

import "time"

// Config is the process-level access log reporting configuration. It lives at
// the top level (sibling of Log) because xray-core writes a single shared
// access stream for all nodes in one process. Per-node auth (NodeID/Key) is
// taken from each node's existing ApiConfig via registration.
type Config struct {
	Enable            bool   `mapstructure:"Enable"`
	ReportInvalidUser *bool  `mapstructure:"ReportInvalidUser"` // nil => true (report probe rows)
	ProbeReportPolicy string `mapstructure:"ProbeReportPolicy"` // broadcast | first | none
	BatchSize         int    `mapstructure:"BatchSize"`
	FlushInterval     string `mapstructure:"FlushInterval"` // duration string, e.g. "3s"
	MaxBatch          int    `mapstructure:"MaxBatch"`
	BufferMax         int    `mapstructure:"BufferMax"` // per-node buffer cap
	IngestChanSize    int    `mapstructure:"IngestChanSize"`
}

// Probe report policies.
const (
	ProbeBroadcast = "broadcast" // send untagged probe rows to every node
	ProbeFirst     = "first"     // send only to the first registered node
	ProbeNone      = "none"      // drop untagged probe rows
)

// normalize fills zero-valued fields with sensible defaults.
func (c *Config) normalize() {
	if c.ReportInvalidUser == nil {
		v := true
		c.ReportInvalidUser = &v
	}
	if c.ProbeReportPolicy == "" {
		c.ProbeReportPolicy = ProbeBroadcast
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 500
	}
	if c.FlushInterval == "" {
		c.FlushInterval = "3s"
	}
	if c.MaxBatch <= 0 {
		c.MaxBatch = 1000
	}
	if c.BufferMax <= 0 {
		c.BufferMax = 50000
	}
	if c.IngestChanSize <= 0 {
		c.IngestChanSize = 100000
	}
}

// flushInterval parses FlushInterval, falling back to 3s on error.
func (c *Config) flushInterval() time.Duration {
	d, err := time.ParseDuration(c.FlushInterval)
	if err != nil || d <= 0 {
		return 3 * time.Second
	}
	return d
}

// reportInvalidUser returns whether untagged probe rows should be reported.
func (c *Config) reportInvalidUser() bool {
	return c.ReportInvalidUser == nil || *c.ReportInvalidUser
}

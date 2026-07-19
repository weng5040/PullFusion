package nodemgr

import "github.com/pullfusion/pullfusion/internal/store"

// Node represents a registry mirror or proxy node.
// Runtime state (latency, speed, score) is computed from DB on demand.
type Node struct {
	URL         string           `json:"url"`
	DisplayName string           `json:"display_name"`
	Enabled     bool             `json:"enabled"`
	Targets     []string         `json:"targets"`
	Token       string           `json:"token,omitempty"`
	Tags        []store.TagEntry `json:"tags"`

	// Computed on demand from DB
	Score       int32   `json:"score"`
	LatencyMs   float64 `json:"latency_ms"`
	SpeedKBps   float64 `json:"speed_kbps"`
	SuccessRate float64 `json:"success_rate"`
	TotalBytes  int64   `json:"total_bytes"`
}

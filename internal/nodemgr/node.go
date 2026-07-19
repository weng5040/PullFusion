package nodemgr

import "sync/atomic"
import "github.com/pullfusion/pullfusion/internal/store"

// Node represents a registry mirror or proxy node.
type Node struct {
	URL         string           `json:"url"`
	DisplayName string           `json:"display_name"`
	Enabled     bool             `json:"enabled"`
	Targets     []string         `json:"targets"`
	Token       string           `json:"token,omitempty"`
	Tags        []store.TagEntry `json:"tags"` // from status.anye.xyz

	// Runtime scoring state
	LatencyMs int64 `json:"latency_ms"`
	InFlight  int32 `json:"inflight"`
	Score     int32 `json:"score"`
	Healthy   bool  `json:"healthy"`
}

func (n *Node) IsIdle() bool   { return atomic.LoadInt32(&n.InFlight) == 0 }
func (n *Node) IncrInflight()   { atomic.AddInt32(&n.InFlight, 1) }
func (n *Node) DecrInflight()   { atomic.AddInt32(&n.InFlight, -1) }

func (n *Node) RecordSuccess(latencyMs int64) {
	atomic.StoreInt64(&n.LatencyMs, latencyMs)
	n.Healthy = true
}

func (n *Node) RecordFailure(latencyMs int64) {
	if latencyMs > 0 {
		atomic.StoreInt64(&n.LatencyMs, latencyMs)
	}
	n.Healthy = false
}

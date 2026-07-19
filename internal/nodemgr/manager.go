package nodemgr

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/pullfusion/pullfusion/internal/config"
	"github.com/pullfusion/pullfusion/internal/store"
)

type Manager struct {
	mu     sync.RWMutex
	nodes  []*Node
	scorer *Scorer
	db     *store.DB
}

func NewManager(cfg *config.Config) *Manager {
	return NewManagerWithStore(cfg, nil)
}

func NewManagerWithStore(cfg *config.Config, database *store.DB) *Manager {
	m := &Manager{scorer: NewScorer(DefaultWeights()), db: database}
	m.initNodes(cfg)
	if database != nil {
		m.loadFromDB()
		m.warmupFromMetrics()
		m.saveToDB()
	}
	slog.Info("node manager initialized", "nodes", len(m.nodes))
	return m
}

func (m *Manager) loadFromDB() {
	dbNodes, err := m.db.LoadNodes()
	if err != nil {
		slog.Warn("failed to load nodes from db", "error", err)
		return
	}
	existing := make(map[string]*Node)
	for _, n := range m.nodes {
		existing[n.URL] = n
	}
	for _, dn := range dbNodes {
		if _, ok := existing[dn.URL]; ok {
			continue
		}
		m.nodes = append(m.nodes, &Node{
			URL: dn.URL, DisplayName: dn.DisplayName,
			Enabled: dn.Enabled, Healthy: true, Targets: dn.Targets, Token: dn.Token, Tags: dn.Tags,
		})
	}
	if len(dbNodes) > 0 {
		slog.Info("loaded node state from database", "db_nodes", len(dbNodes))
	}
}

func (m *Manager) warmupFromMetrics() {
	latest, err := m.db.LoadLatestMetrics()
	if err != nil || len(latest) == 0 {
		return
	}
	for _, n := range m.nodes {
		r, ok := latest[n.URL]
		if !ok {
			continue
		}
		n.LatencyMs = r.LatencyMs
		n.Healthy = r.Healthy
		m.scorer.Score(n)
	}
	slog.Info("warmup: restored metrics", "nodes", len(latest))
}

func (m *Manager) saveToDB() {
	var records []store.NodeRecord
	for _, n := range m.List() {
		records = append(records, store.NodeRecord{
			URL: n.URL, DisplayName: n.DisplayName,
			Enabled: n.Enabled, Targets: n.Targets, Token: n.Token, Tags: n.Tags,
		})
	}
	if err := m.db.SaveNodes(records); err != nil {
		slog.Warn("failed to save nodes to db", "error", err)
	}
}

func (m *Manager) List() []*Node {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r := make([]*Node, len(m.nodes))
	copy(r, m.nodes)
	return r
}

func (m *Manager) AddNode(node *Node) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, n := range m.nodes {
		if n.URL == node.URL {
			return
		}
	}
	node.Healthy = true
	m.nodes = append(m.nodes, node)
}

func (m *Manager) SelectBest(registry string) *Node {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var candidates []*Node
	for _, n := range m.nodes {
		if !n.Enabled || !n.Healthy {
			continue
		}
		tm := len(n.Targets) == 0
		for _, t := range n.Targets {
			if t == registry {
				tm = true
				break
			}
		}
		if tm {
			candidates = append(candidates, n)
		}
	}
	sel := m.scorer.SelectBest(candidates)
	if sel != nil {
		sel.IncrInflight()
		slog.Info("node selected", "name", sel.DisplayName, "score", sel.Score, "inflight", sel.InFlight, "latency_ms", sel.LatencyMs)
	}
	return sel
}

func (m *Manager) ReleaseNode(node *Node, success bool, latencyMs, speedKBps int64) {
	if node == nil {
		return
	}
	node.DecrInflight()
	if success {
		node.RecordSuccess(latencyMs)
	} else {
		node.RecordFailure(latencyMs)
	}
	m.scorer.Score(node)

	if m.db != nil {
		r := store.MetricRecord{
			NodeURL: node.URL, Timestamp: time.Now(),
			LatencyMs: latencyMs, SpeedKBps: speedKBps,
			Success: success, Score: node.Score,
			InFlight: node.InFlight, Healthy: node.Healthy,
		}
		m.db.InsertMetric(r)
		m.saveToDB()
	}
}

func (m *Manager) GetHealthStatus() (int, int) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	h := 0
	for _, n := range m.nodes {
		if n.Healthy {
			h++
		}
	}
	return len(m.nodes), h
}

func (m *Manager) GetStats() (map[string][]store.AggregatedStats, error) {
	if m.db == nil {
		return nil, fmt.Errorf("no database")
	}
	return m.db.GetStats()
}

func (m *Manager) ReloadNodes(rawCfg interface{}) {
	cfg, ok := rawCfg.(*config.Config)
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nodes = nil
	m.initNodes(cfg)
}

func (m *Manager) GetScoredNodes() []*Node {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, n := range m.nodes {
		m.scorer.Score(n)
	}
	r := make([]*Node, len(m.nodes))
	copy(r, m.nodes)
	return r
}

func (m *Manager) initNodes(cfg *config.Config) {
	for _, mirror := range cfg.Mirrors.Dockerhub {
		m.nodes = append(m.nodes, &Node{
			URL: mirror.URL, DisplayName: mirror.DisplayName,
			Enabled: true, Healthy: true, Targets: []string{"dockerhub"}, Token: mirror.Token,
		})
	}
	for _, mirror := range cfg.Mirrors.Ghcr {
		m.nodes = append(m.nodes, &Node{
			URL: mirror.URL, DisplayName: mirror.DisplayName,
			Enabled: true, Healthy: true, Targets: []string{"ghcr"}, Token: mirror.Token,
		})
	}
	slog.Info(fmt.Sprintf("loaded %d nodes from config", len(m.nodes)))
}

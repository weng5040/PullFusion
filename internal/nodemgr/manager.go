package nodemgr

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/pullfusion/pullfusion/internal/config"
	"github.com/pullfusion/pullfusion/internal/store"
)

type Manager struct {
	mu    sync.RWMutex
	nodes []*Node
	db    *store.DB
}

func NewManager(cfg *config.Config) *Manager {
	return NewManagerWithStore(cfg, nil)
}

func NewManagerWithStore(cfg *config.Config, database *store.DB) *Manager {
	m := &Manager{db: database}
	m.initNodes(cfg)
	if database != nil {
		m.loadFromDB()
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
			Enabled: dn.Enabled, Targets: dn.Targets,
			Token: dn.Token, Tags: dn.Tags,
		})
	}
	if len(dbNodes) > 0 {
		slog.Info("loaded nodes from database", "count", len(dbNodes))
	}
}

func (m *Manager) saveToDB() {
	var records []store.NodeRecord
	for _, n := range m.List() {
		records = append(records, store.NodeRecord{
			URL: n.URL, DisplayName: n.DisplayName,
			Enabled: n.Enabled, Targets: n.Targets,
			Token: n.Token, Tags: n.Tags,
		})
	}
	if err := m.db.SaveNodes(records); err != nil {
		slog.Warn("failed to save nodes", "error", err)
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
	m.nodes = append(m.nodes, node)
}

// GetScoredNodes returns all nodes with scores computed from DB.
func (m *Manager) GetDB() *store.DB { return m.db }
func (m *Manager) GetScoredNodes() []*Node {
	m.mu.RLock()
	nodes := make([]*Node, len(m.nodes))
	copy(nodes, m.nodes)
	m.mu.RUnlock()

	for _, n := range nodes {
		if m.db != nil {
			ss := m.db.ComputeScore(n.URL)
			n.Score = ss.Score
			n.LatencyMs = ss.Latency
			n.SpeedKBps = ss.Speed
			n.SuccessRate = ss.Success
			n.TotalBytes = ss.Bytes
		}
	}
	return nodes
}

// SelectBest picks the node with the highest DB-computed score.
// Only considers enabled nodes that have been tested (score > 0).
func (m *Manager) SelectBest(registry string) *Node {
	m.mu.RLock()
	var candidates []*Node
	for _, n := range m.nodes {
		if !n.Enabled {
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
	m.mu.RUnlock()

	var best *Node
	var bestScore int32
	for _, n := range candidates {
		if m.db != nil {
			ss := m.db.ComputeScore(n.URL)
			n.Score = ss.Score
			if ss.Score >= bestScore {
				bestScore = ss.Score
				best = n
			}
		} else if best == nil {
			best = n
		}
	}
	if best != nil {
		slog.Info("node selected", "name", best.DisplayName, "score", best.Score)
	}
	return best
}

// RecordDownload writes all event metrics to the DB after a download completes.
func (m *Manager) RecordDownload(nodeURL string, latencyMs, speedKBps, byteKB int64, success bool) {
	if m.db == nil {
		return
	}
	if err := m.db.InsertDownloadEvents(nodeURL, latencyMs, speedKBps, byteKB, success); err != nil {
		slog.Warn("record download failed", "url", nodeURL, "error", err)
		return
	}
	status := "success"
	if !success {
		status = "failure"
	}
	slog.Info("download recorded", "url", nodeURL[:min(len(nodeURL), 50)], "status", status, "latency_ms", latencyMs, "speed_kbps", speedKBps)
}

func (m *Manager) GetHealthStatus() (int, int) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	h := 0
	for _, n := range m.nodes {
		if n.Enabled {
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

func (m *Manager) PersistNodes() {
	if m.db != nil {
		m.saveToDB()
	}
}

func (m *Manager) initNodes(cfg *config.Config) {
	for _, mirror := range cfg.Mirrors.Dockerhub {
		m.nodes = append(m.nodes, &Node{
			URL: mirror.URL, DisplayName: mirror.DisplayName,
			Enabled: true, Targets: []string{"dockerhub"}, Token: mirror.Token,
		})
	}
	for _, mirror := range cfg.Mirrors.Ghcr {
		m.nodes = append(m.nodes, &Node{
			URL: mirror.URL, DisplayName: mirror.DisplayName,
			Enabled: true, Targets: []string{"ghcr"}, Token: mirror.Token,
		})
	}
	slog.Info(fmt.Sprintf("loaded %d nodes from config", len(m.nodes)))
}

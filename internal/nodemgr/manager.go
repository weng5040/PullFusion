package nodemgr

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/pullfusion/pullfusion/internal/config"
)

// Manager manages registry mirror nodes.
type Manager struct {
	mu    sync.RWMutex
	nodes []*Node
}

// NewManager creates a node manager, loading nodes from config.
func NewManager(cfg *config.Config) *Manager {
	m := &Manager{}
	m.initNodes(cfg)
	return m
}

// List returns a copy of all nodes.
func (m *Manager) List() []*Node {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*Node, len(m.nodes))
	copy(result, m.nodes)
	return result
}

// AddNode adds a new node (for fetched nodes).
func (m *Manager) AddNode(node *Node) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, n := range m.nodes {
		if n.URL == node.URL {
			return // duplicate
		}
	}
	m.nodes = append(m.nodes, node)
}

// ReloadNodes reloads nodes from config.
func (m *Manager) ReloadNodes(rawCfg interface{}) {
	cfg, ok := rawCfg.(*config.Config)
	if !ok { return }
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nodes = nil
	m.initNodes(cfg)
}

// GetHealthStatus returns total and healthy node counts.
func (m *Manager) GetHealthStatus() (int, int) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	healthy := 0
	for _, n := range m.nodes {
		if n.Healthy {
			healthy++
		}
	}
	return len(m.nodes), healthy
}

func (m *Manager) initNodes(cfg *config.Config) {
	for _, mirror := range cfg.Mirrors.Dockerhub {
		m.nodes = append(m.nodes, &Node{
			URL:         mirror.URL,
			DisplayName: mirror.DisplayName,
			Type:        NodeTypeMirror,
			Priority:    mirror.Priority,
			Enabled:     true,
			Healthy:     true,
			Targets:     []string{"dockerhub"},
			Token:       mirror.Token,
		})
	}
	for _, mirror := range cfg.Mirrors.Ghcr {
		m.nodes = append(m.nodes, &Node{
			URL:         mirror.URL,
			DisplayName: mirror.DisplayName,
			Type:        NodeTypeMirror,
			Priority:    mirror.Priority,
			Enabled:     true,
			Healthy:     true,
			Targets:     []string{"ghcr"},
			Token:       mirror.Token,
		})
	}
	slog.Info(fmt.Sprintf("loaded %d nodes from config", len(m.nodes)))
}

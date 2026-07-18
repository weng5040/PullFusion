package nodemgr

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pullfusion/pullfusion/internal/config"
	"github.com/pullfusion/pullfusion/internal/metrics"
)

// Manager 节点管理器
type Manager struct {
	nodes    []*Node
	mu       sync.RWMutex
	cfg      *config.Config
	balancer *Balancer
	tester   *SpeedTester
}

// NewManager 创建节点管理器，从配置构建节点清单
func NewManager(cfg *config.Config) *Manager {
	m := &Manager{
		cfg:      cfg,
		balancer: NewDefaultBalancer(),
		nodes:    make([]*Node, 0),
	}
	m.initNodes(cfg)
	return m
}

// initNodes 从配置初始化节点列表（mirrors + proxies + builtin）
func (m *Manager) initNodes(cfg *config.Config) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.nodes = make([]*Node, 0)

	if cfg == nil {
		return
	}

	// Dockerhub mirrors
	for _, mirror := range cfg.Mirrors.Dockerhub {
		m.nodes = append(m.nodes, &Node{
			URL:         mirror.URL,
			DisplayName: mirror.DisplayName,
			Type:        NodeTypeMirror,
			Priority:    mirror.Priority,
			Enabled:     true,
			Healthy:     true,
			Targets:     []string{"dockerhub"},
		})
	}

	// Direct dockerhub (lowest priority fallback)
	m.nodes = append(m.nodes, &Node{
		URL:         "https://registry-1.docker.io",
		DisplayName: "dockerhub-direct",
		Type:        NodeTypeMirror,
		Priority:    1,
		Enabled:     true,
		Healthy:     true,
		Targets:     []string{"dockerhub"},
	})

	// Ghcr mirrors
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

	// Proxy nodes (socks5/http)
	for _, proxy := range cfg.Proxies.Nodes {
		nodeType := NodeTypeSocks5
		if len(proxy.URL) >= 4 && proxy.URL[:4] == "http" {
			nodeType = NodeTypeHTTP
		}
		m.nodes = append(m.nodes, &Node{
			URL:         proxy.URL,
			DisplayName: proxy.DisplayName,
			Type:        nodeType,
			Priority:    proxy.Priority,
			Enabled:     cfg.Proxies.Enabled,
			Healthy:     true,
			Targets:     proxy.Targets,
		})
	}

	// Builtin mirrors — 去重合并
	seen := make(map[string]bool)
	for _, n := range m.nodes {
		seen[n.URL] = true
	}

	// Builtin dockerhub
	for _, url := range cfg.Builtin.Dockerhub {
		if seen[url] {
			continue
		}
		seen[url] = true
		m.nodes = append(m.nodes, &Node{
			URL:         url,
			DisplayName: url,
			Type:        NodeTypeMirror,
			Priority:    1, // 内置节点最低优先级
			Enabled:     true,
			Healthy:     true,
			Targets:     []string{"dockerhub"},
		})
	}

	// Builtin ghcr
	for _, url := range cfg.Builtin.Ghcr {
		if seen[url] {
			continue
		}
		seen[url] = true
		m.nodes = append(m.nodes, &Node{
			URL:         url,
			DisplayName: url,
			Type:        NodeTypeMirror,
			Priority:    1,
			Enabled:     true,
			Healthy:     true,
			Targets:     []string{"ghcr"},
		})
	}
}

// ReloadNodes 热加载重建节点列表
func (m *Manager) ReloadNodes(cfgRaw interface{}) {
	cfg, ok := cfgRaw.(*config.Config)
	if !ok {
		return
	}
	m.cfg = cfg
	m.initNodes(cfg)
}

// List 返回所有节点
func (m *Manager) List() []*Node {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*Node, len(m.nodes))
	copy(result, m.nodes)
	return result
}

// SelectForBlob 为 blob 下载选择 k 个最佳节点
func (m *Manager) SelectForBlob(registry string, expectedSize int64, k int) []*Node {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var candidates []*Node
	for _, n := range m.nodes {
		if !n.Enabled || !n.Healthy {
			continue
		}
		// 检查 targets 是否匹配
		targetMatch := len(n.Targets) == 0
		for _, t := range n.Targets {
			if t == registry {
				targetMatch = true
				break
			}
		}
		if !targetMatch {
			continue
		}
		candidates = append(candidates, n)
	}

	if len(candidates) == 0 {
		return nil
	}
	if len(candidates) <= k {
		return candidates
	}

	return m.balancer.TopK(candidates, k)
}

// MarkFailed 标记节点失败，连续失败 N 次后熔断，更新 Prometheus metrics
func (m *Manager) MarkFailed(node *Node) {
	node.mu.Lock()
	defer node.mu.Unlock()
	node.FailCount++
	node.LastCheck = time.Now()
	if node.FailCount >= 3 {
		node.Healthy = false
	}
	metrics.NodeHealth.WithLabelValues(node.DisplayName).Set(0)
}

// MarkSuccess 标记节点成功，恢复健康并更新 Prometheus metrics
func (m *Manager) MarkSuccess(node *Node) {
	node.mu.Lock()
	defer node.mu.Unlock()
	node.FailCount = 0
	node.Healthy = true
	node.LastCheck = time.Now()
	metrics.NodeHealth.WithLabelValues(node.DisplayName).Set(1)
	metrics.NodeSpeed.WithLabelValues(node.DisplayName).Set(node.Speed)
}

// IncrInflight 增加并发计数并更新 Prometheus metrics
func (m *Manager) IncrInflight(node *Node) {
	atomic.AddInt32(&node.InFlight, 1)
	metrics.NodeInflight.WithLabelValues(node.DisplayName).Set(float64(atomic.LoadInt32(&node.InFlight)))
}

// DecrInflight 减少并发计数并更新 Prometheus metrics
func (m *Manager) DecrInflight(node *Node) {
	atomic.AddInt32(&node.InFlight, -1)
	metrics.NodeInflight.WithLabelValues(node.DisplayName).Set(float64(atomic.LoadInt32(&node.InFlight)))
}

// StartSpeedTest 启动后台测速
func (m *Manager) StartSpeedTest(ctx context.Context) {
	testURL := ""
	intervalSec := 300
	if m.cfg != nil {
		testURL = m.cfg.Downloader.SpeedTestURL
		intervalSec = int(m.cfg.Downloader.SpeedTestInterval.Seconds())
		if intervalSec <= 0 {
			intervalSec = 300
		}
	}
	m.tester = NewSpeedTester(testURL, intervalSec)
	go m.tester.Start(ctx, m.List())
}

// TestSingleNode 手动对单个节点测速（异步执行）
func (m *Manager) TestSingleNode(node *Node) {
	if m.tester == nil {
		return
	}
	go func() {
		m.tester.testSingle(node, m)
	}()
}

// StartHealthProbe 启动后台健康探测
func (m *Manager) StartHealthProbe(ctx context.Context) {
	checker := NewHealthChecker(30)
	go checker.StartProbeLoop(ctx, m)
}

// GetHealthStatus 获取健康状态统计
func (m *Manager) GetHealthStatus() (total, healthy int) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, n := range m.nodes {
		total++
		if n.Healthy {
			healthy++
		}
	}
	return
}

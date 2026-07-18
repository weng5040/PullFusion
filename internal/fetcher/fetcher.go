package fetcher

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/pullfusion/pullfusion/internal/nodemgr"
)

// ProxyItem 状态 API 返回的节点项
type ProxyItem struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	Access     string `json:"access"`
	Selectable bool   `json:"selectable"`
	Status     string `json:"status"`
}

// FetchResult 抓取合并结果
type FetchResult struct {
	Fetched int      `json:"fetched"` // 本次抓取到的原始数量
	Added   int      `json:"added"`   // 实际新增数量
	Total   int      `json:"total"`   // 最终总节点数
	Nodes   []string `json:"nodes"`   // 新增节点名称列表
	Elapsed string   `json:"elapsed"` // 耗时
}

var httpClient = &http.Client{Timeout: 15 * time.Second}

// FetchFromStatus 从 status.anye.xyz 抓取免费节点
func FetchFromStatus(ctx context.Context, types []string) ([]ProxyItem, error) {
	var allItems []ProxyItem

	for _, t := range types {
		url := fmt.Sprintf("https://status.anye.xyz/status/%s", t)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("create request for %s: %w", t, err)
		}
		req.Header.Set("Accept", "application/json")

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", t, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("fetch %s: HTTP %d", t, resp.StatusCode)
		}

		var items []ProxyItem
		if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
			return nil, fmt.Errorf("decode %s: %w", t, err)
		}

		allItems = append(allItems, items...)
	}

	return allItems, nil
}

// targetMap 类型到默认 targets 的映射
var targetMap = map[string][]string{
	"hub":  {"dockerhub"},
	"ghcr": {"ghcr"},
}

// MergeIntoManager 过滤合并到节点管理器
// 过滤条件：selectable=true && access=public && status=online
func MergeIntoManager(items []ProxyItem, mgr *nodemgr.Manager, existing map[string]bool) FetchResult {
	start := time.Now()
	result := FetchResult{
		Fetched: len(items),
	}

	for _, item := range items {
		// 过滤：仅保留可选、公开、在线节点
		if !item.Selectable || item.Access != "public" || item.Status != "online" {
			continue
		}

		// 去重：URL 已在现有节点中
		if existing[item.URL] {
			continue
		}

		targets := determineTargets(item)
		node := &nodemgr.Node{
			URL:         item.URL,
			DisplayName: item.Name,
			Type:        nodemgr.NodeTypeMirror,
			Priority:    50, // 低于手动配置节点
			Enabled:     true,
			Healthy:     true,
			Targets:     targets,
		}

		mgr.AddNode(node)
		existing[item.URL] = true
		result.Added++
		result.Nodes = append(result.Nodes, item.Name)
	}

	nodes := mgr.List()
	result.Total = len(nodes)
	result.Elapsed = time.Since(start).Round(time.Millisecond).String()

	return result
}

// determineTargets 根据名称/URL 推断目标 registry 类型
func determineTargets(item ProxyItem) []string {
	lowerName := strings.ToLower(item.Name)
	lowerURL := strings.ToLower(item.URL)
	if strings.Contains(lowerName, "ghcr") || strings.Contains(lowerURL, "ghcr") {
		return []string{"ghcr"}
	}
	return []string{"dockerhub"}
}

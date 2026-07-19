package fetcher

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/pullfusion/pullfusion/internal/nodemgr"
)

// defaultTypes are the registry types to fetch from status.anye.xyz
var defaultTypes = []string{"hub", "ghcr"}

// ProxyItem is a single mirror entry from status.anye.xyz
type ProxyItem struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	Access     string `json:"access"`
	Selectable bool   `json:"selectable"`
	Status     string `json:"status"`
}

// FetchResult summarizes a fetch operation.
type FetchResult struct {
	Fetched int      `json:"fetched"`
	Added   int      `json:"added"`
	Total   int      `json:"total"`
	Nodes   []string `json:"nodes"`
	Elapsed string   `json:"elapsed"`
}

// FetchFromStatus fetches mirror nodes from status.anye.xyz.
func FetchFromStatus(ctx context.Context, types []string) ([]ProxyItem, error) {
	var all []ProxyItem
	for _, t := range types {
		url := fmt.Sprintf("https://status.anye.xyz/status/%s", t)
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("create request for %s: %w", t, err)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", t, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("fetch %s: HTTP %d", t, resp.StatusCode)
		}

		var items []ProxyItem
		if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
			return nil, fmt.Errorf("decode %s: %w", t, err)
		}
		all = append(all, items...)
	}
	return all, nil
}

// MergeIntoManager filters and imports nodes into the node manager.
func MergeIntoManager(items []ProxyItem, mgr *nodemgr.Manager, existing map[string]bool) FetchResult {
	start := time.Now()
	var result FetchResult

	for _, item := range items {
		result.Fetched++
		if !item.Selectable || item.Access != "public" || item.Status != "online" {
			continue
		}

		item.URL = strings.TrimRight(item.URL, "/")
		if existing[item.URL] {
			continue
		}
		existing[item.URL] = true

		targets := determineTargets(item.Name, item.URL)
		mgr.AddNode(&nodemgr.Node{
			URL:         item.URL,
			DisplayName: item.Name,
			Type:        nodemgr.NodeTypeMirror,
			Priority:    50,
			Enabled:     true,
			Healthy:     true,
			Targets:     targets,
		})
		result.Added++
		result.Nodes = append(result.Nodes, item.Name)
	}

	result.Total = len(mgr.List())
	result.Elapsed = time.Since(start).String()
	slog.Info("fetcher: merged nodes", "fetched", result.Fetched, "added", result.Added, "total", result.Total)
	return result
}

// FetchAndMerge fetches from status.anye.xyz and merges into the node manager.
// Set persist to true to enable Save callback after fetch.
type SaveFunc func(*nodemgr.Manager, interface{}) error

func FetchAndMerge(ctx context.Context, mgr *nodemgr.Manager, saveFn func() error) (FetchResult, error) {
	items, err := FetchFromStatus(ctx, defaultTypes)
	if err != nil {
		return FetchResult{}, err
	}

	existing := make(map[string]bool)
	for _, n := range mgr.List() {
		existing[strings.TrimRight(n.URL, "/")] = true
	}

	result := MergeIntoManager(items, mgr, existing)

	if saveFn != nil && result.Added > 0 {
		if err := saveFn(); err != nil {
			slog.Warn("persist after fetch failed", "error", err)
		}
	}

	return result, nil
}

// determineTargets guesses the registry targets from the node name/URL.
func determineTargets(name, url string) []string {
	nameLower := strings.ToLower(name)
	urlLower := strings.ToLower(url)

	if strings.Contains(nameLower, "ghcr") || strings.Contains(urlLower, "ghcr") {
		return []string{"ghcr"}
	}
	if strings.Contains(nameLower, "gcr") || strings.Contains(urlLower, "gcr.io") {
		return []string{"gcr"}
	}
	return []string{"dockerhub"}
}

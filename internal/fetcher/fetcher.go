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
	"github.com/pullfusion/pullfusion/internal/store"
)

// defaultTypes are the registry types to fetch from status.anye.xyz
var defaultTypes = []string{"hub", "ghcr"}

// ProxyItem is a single mirror entry from status.anye.xyz
type ProxyItem struct {
	Name       string   `json:"name"`
	URL        string   `json:"url"`
	Tags       []struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	} `json:"tags"`       // labels like "cloudflare", "official", etc.
	Access     string   `json:"access"`     // "public" or "private"
	Selectable bool     `json:"selectable"` // can be used as a mirror
	Official   bool     `json:"official"`   // official Docker mirror (exclude)
	Status     string   `json:"status"`     // current status (accepted regardless)
	LastCheck  string   `json:"lastCheck"`  // ISO timestamp of last check
}

// FetchResult summarizes a fetch operation.
type FetchResult struct {
	Fetched   int      `json:"fetched"`
	Skipped   int      `json:"skipped"`
	Added     int      `json:"added"`
	Total     int      `json:"total"`
	Nodes     []string `json:"nodes"`
	Elapsed   string   `json:"elapsed"`
}

// FetchFromStatus fetches mirror nodes from status.anye.xyz.
func FetchFromStatus(ctx context.Context, types []string) ([]ProxyItem, error) {
	var all []ProxyItem
	for _, t := range types {
		u := fmt.Sprintf("https://status.anye.xyz/status/%s", t)
		slog.Info("fetcher: requesting nodes", "type", t, "url", u)

		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			return nil, fmt.Errorf("create request for %s: %w", t, err)
		}

		start := time.Now()
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
		slog.Info("fetcher: raw nodes received", "type", t, "count", len(items), "duration_ms", time.Since(start).Milliseconds())
		all = append(all, items...)
	}
	return all, nil
}

// MergeIntoManager filters and imports nodes into the node manager.
// Filtering rules:
//   - access: only "public"
//   - selectable: only true
//   - official: only false (exclude official Docker mirrors)
//   - status: accepted regardless (status.anye.xyz may report offline, but local test may succeed)
//   - tags: stored as part of the node name for display
func MergeIntoManager(items []ProxyItem, mgr *nodemgr.Manager, existing map[string]bool) FetchResult {
	start := time.Now()
	var result FetchResult

	for _, item := range items {
		result.Fetched++

		// Filter: access must be "public"
		if item.Access != "public" {
			slog.Debug("fetcher: skip non-public", "name", item.Name, "access", item.Access)
			result.Skipped++
			continue
		}

		// Filter: must be selectable
		if !item.Selectable {
			slog.Debug("fetcher: skip not selectable", "name", item.Name)
			result.Skipped++
			continue
		}

		// Filter: exclude official mirrors (they have their own auth)
		if item.Official {
			slog.Debug("fetcher: skip official", "name", item.Name)
			result.Skipped++
			continue
		}

		// URL cleanup
		item.URL = strings.TrimRight(item.URL, "/")
		if item.URL == "" {
			slog.Debug("fetcher: skip empty url", "name", item.Name)
			result.Skipped++
			continue
		}

		// Deduplicate by URL
		if existing[item.URL] {
			result.Skipped++
			continue
		}
		existing[item.URL] = true

		// Build display name: include tags for context
		displayName := item.Name
		tagNames := make([]string, len(item.Tags))
		for i, t := range item.Tags { tagNames[i] = t.Name }
		tagStr := strings.Join(tagNames, ",")
		if tagStr != "" {
			displayName = item.Name // keep clean, tags stored separately
		}

		targets := determineTargets(item.Name, item.URL)
		// Convert tags from ProxyItem to store.TagEntry
		tagEntries := make([]store.TagEntry, len(item.Tags))
		for i, t := range item.Tags {
			tagEntries[i] = store.TagEntry{Name: t.Name, Color: t.Color}
		}
		mgr.AddNode(&nodemgr.Node{
			URL:         item.URL,
			DisplayName: displayName,
			Enabled:     true,
			Targets:     targets,
			Tags:        tagEntries,
		})
		result.Added++
		result.Nodes = append(result.Nodes, item.Name)

		slog.Info("fetcher: node added",
			"name", item.Name,
			"url", item.URL[:min(len(item.URL), 60)],
			"tags", tagStr,
			"status", item.Status,
			"last_check", item.LastCheck,
		)
	}

	result.Total = len(mgr.List())
	result.Elapsed = time.Since(start).String()
	slog.Info("fetcher: merge complete",
		"fetched", result.Fetched,
		"skipped", result.Skipped,
		"added", result.Added,
		"total", result.Total,
		"duration_ms", time.Since(start).Milliseconds(),
	)
	return result
}

// FetchAndMerge fetches from status.anye.xyz and merges into the node manager.
// If saveFn is provided, persists after successful fetch with new nodes.
func FetchAndMerge(ctx context.Context, mgr *nodemgr.Manager, saveFn func() error) (FetchResult, error) {
	slog.Info("fetcher: starting fetch from status.anye.xyz")
	items, err := FetchFromStatus(ctx, defaultTypes)
	if err != nil {
		slog.Error("fetcher: fetch failed", "error", err)
		return FetchResult{}, err
	}

	existing := make(map[string]bool)
	for _, n := range mgr.List() {
		existing[strings.TrimRight(n.URL, "/")] = true
	}

	result := MergeIntoManager(items, mgr, existing)

	if saveFn != nil && result.Added > 0 {
		if err := saveFn(); err != nil {
			slog.Warn("fetcher: persist after fetch failed", "error", err)
		} else {
			slog.Info("fetcher: nodes persisted", "added", result.Added)
		}
	}

	return result, nil
}

// determineTargets guesses the registry targets from the node name/URL.
func determineTargets(name, url string) []string {
	nl := strings.ToLower(name)
	ul := strings.ToLower(url)

	if strings.Contains(nl, "ghcr") || strings.Contains(ul, "ghcr") {
		return []string{"ghcr"}
	}
	if strings.Contains(nl, "gcr") || strings.Contains(ul, "gcr.io") {
		return []string{"gcr"}
	}
	return []string{"dockerhub"}
}

package persist

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/pullfusion/pullfusion/internal/config"
	"github.com/pullfusion/pullfusion/internal/nodemgr"
)

const defaultFile = "data/nodes.json"

// NodeEntry is the persisted form of a node.
type NodeEntry struct {
	URL         string   `json:"url"`
	DisplayName string   `json:"display_name"`
	Targets     []string `json:"targets"`
	Token       string   `json:"token,omitempty"`
}

// Save writes the current node list to a JSON file.
func Save(mgr *nodemgr.Manager, cfg *config.Config) error {
	path := getPath(cfg)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	var entries []NodeEntry
	for _, n := range mgr.List() {
		// Skip nodes with speed>0 that came from config (those are manual overrides)
		entries = append(entries, NodeEntry{
			URL:         n.URL,
			DisplayName: n.DisplayName,
			Targets:     n.Targets,
			Token:       n.Token,
		})
	}

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}

	slog.Info("persist: saved nodes", "count", len(entries), "path", path)
	return os.WriteFile(path, data, 0644)
}

// Load reads persisted nodes from JSON and adds them to the node manager.
// Returns the number of nodes loaded.
func Load(mgr *nodemgr.Manager, cfg *config.Config) (int, error) {
	path := getPath(cfg)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	var entries []NodeEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return 0, err
	}

	count := 0
	for _, e := range entries {
		mgr.AddNode(&nodemgr.Node{
			URL:         e.URL,
			DisplayName: e.DisplayName,
			Enabled:     true,
			Targets:     e.Targets,
			Token:       e.Token,
		})
		count++
	}

	slog.Info("persist: loaded nodes", "count", count, "path", path)
	return count, nil
}

func getPath(cfg *config.Config) string {
	if cfg != nil && cfg.Downloader.CacheDir != "" {
		return filepath.Join(filepath.Dir(cfg.Downloader.CacheDir), "nodes.json")
	}
	return defaultFile
}

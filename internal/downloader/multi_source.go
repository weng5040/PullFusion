package downloader

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/pullfusion/pullfusion/internal/nodemgr"
)

// MultiSourceDownloader handles blob downloads with smart node selection.
type MultiSourceDownloader struct {
	nodeMgr *nodemgr.Manager
}

// DownloadRequest represents a blob download request.
type DownloadRequest struct {
	Name     string
	Digest   string
	Registry string
	Token    string
}

// NewMultiSourceDownloader creates a new downloader.
func NewMultiSourceDownloader(nodeMgr *nodemgr.Manager) *MultiSourceDownloader {
	return &MultiSourceDownloader{nodeMgr: nodeMgr}
}

// Download fetches a blob using the best available node with full logging.
func (d *MultiSourceDownloader) Download(ctx context.Context, req DownloadRequest) (io.ReadCloser, int64, int, error) {
	reqStart := time.Now()

	// Node selection with detailed logging
	node := d.nodeMgr.SelectBest(req.Registry)
	if node == nil {
		slog.Error("download no healthy node",
			"name", req.Name,
			"digest", req.Digest[:19],
			"registry", req.Registry,
			"duration_ms", time.Since(reqStart).Milliseconds(),
		)
		return nil, 0, 0, fmt.Errorf("no healthy node available for %s", req.Registry)
	}

	blobURL := fmt.Sprintf("%s/v2/%s/blobs/%s", node.URL, req.Name, req.Digest)
	slog.Info("download start",
		"name", req.Name,
		"digest", req.Digest[:19],
		"node", node.DisplayName,
		"node_url", node.URL[:min(len(node.URL), 50)],
		"node_score", node.Score,
		"has_token", req.Token != "" || node.Token != "",
	)

	// Build HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "GET", blobURL, nil)
	if err != nil {
		slog.Error("download create request failed", "node", node.DisplayName, "error", err)
		d.nodeMgr.ReleaseNode(node, false, 0, 0)
		return nil, 0, 0, fmt.Errorf("create request: %w", err)
	}

	// Token injection: request-level priority over node-level
	if req.Token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+req.Token)
	} else if node.Token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+node.Token)
	}
	httpReq.Header.Set("User-Agent", "PullFusion/1.0")

	// Execute download
	connStart := time.Now()
	resp, err := http.DefaultClient.Do(httpReq)
	connDuration := time.Since(connStart)
	totalDuration := time.Since(reqStart)

	if err != nil {
		slog.Error("download connection failed",
			"name", req.Name,
			"node", node.DisplayName,
			"error", err,
			"conn_ms", connDuration.Milliseconds(),
			"total_ms", totalDuration.Milliseconds(),
		)
		d.nodeMgr.ReleaseNode(node, false, connDuration.Milliseconds(), 0)
		return nil, 0, 0, fmt.Errorf("download from %s: %w", node.DisplayName, err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		slog.Warn("download upstream error",
			"name", req.Name,
			"node", node.DisplayName,
			"status", resp.StatusCode,
			"conn_ms", connDuration.Milliseconds(),
		)
		d.nodeMgr.ReleaseNode(node, false, connDuration.Milliseconds(), 0)
		return nil, 0, 0, fmt.Errorf("upstream %s returned HTTP %d", node.DisplayName, resp.StatusCode)
	}

	// Calculate approximate speed (bytes / ms)
	speedKBps := int64(0)
	if resp.ContentLength > 0 && connDuration.Milliseconds() > 0 {
		speedKBps = resp.ContentLength / connDuration.Milliseconds()
	}
	d.nodeMgr.ReleaseNode(node, true, connDuration.Milliseconds(), speedKBps)

	slog.Info("download stream ready",
		"name", req.Name,
		"digest", req.Digest[:19],
		"node", node.DisplayName,
		"size", resp.ContentLength,
		"conn_ms", connDuration.Milliseconds(),
		"speed_kbps", speedKBps,
	)

	return resp.Body, resp.ContentLength, 1, nil
}

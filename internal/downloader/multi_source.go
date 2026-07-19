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

type MultiSourceDownloader struct {
	nodeMgr *nodemgr.Manager
}

type DownloadRequest struct {
	Name     string
	Digest   string
	Registry string
	Token    string
}

func NewMultiSourceDownloader(nodeMgr *nodemgr.Manager) *MultiSourceDownloader {
	return &MultiSourceDownloader{nodeMgr: nodeMgr}
}

func (d *MultiSourceDownloader) Download(ctx context.Context, req DownloadRequest) (io.ReadCloser, int64, int, error) {
	node := d.nodeMgr.SelectBest(req.Registry)
	if node == nil {
		return nil, 0, 0, fmt.Errorf("no node available for %s", req.Registry)
	}

	blobURL := fmt.Sprintf("%s/v2/%s/blobs/%s", node.URL, req.Name, req.Digest)
	slog.Info("download start", "name", req.Name, "digest", req.Digest[:19], "node", node.DisplayName, "score", node.Score)

	httpReq, err := http.NewRequestWithContext(ctx, "GET", blobURL, nil)
	if err != nil {
		d.nodeMgr.RecordDownload(node.URL, 0, 0, 0, false)
		return nil, 0, 0, err
	}

	if req.Token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+req.Token)
	} else if node.Token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+node.Token)
	}
	httpReq.Header.Set("User-Agent", "PullFusion/1.0")

	start := time.Now()
	resp, err := http.DefaultClient.Do(httpReq)
	latencyMs := time.Since(start).Milliseconds()

	if err != nil {
		d.nodeMgr.RecordDownload(node.URL, latencyMs, 0, 0, false)
		return nil, 0, 0, fmt.Errorf("download from %s: %w", node.DisplayName, err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		d.nodeMgr.RecordDownload(node.URL, latencyMs, 0, 0, false)
		return nil, 0, 0, fmt.Errorf("upstream %s returned HTTP %d", node.DisplayName, resp.StatusCode)
	}

	speedKBps := int64(0)
	byteKB := resp.ContentLength / 1024
	if latencyMs > 0 && resp.ContentLength > 0 {
		speedKBps = resp.ContentLength / latencyMs // bytes/ms ≈ KB/s
	}

	d.nodeMgr.RecordDownload(node.URL, latencyMs, speedKBps, byteKB, true)

	slog.Info("download ok", "name", req.Name, "node", node.DisplayName, "size", resp.ContentLength, "latency_ms", latencyMs, "speed_kbps", speedKBps)

	return resp.Body, resp.ContentLength, 1, nil
}

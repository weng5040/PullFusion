package downloader

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/pullfusion/pullfusion/internal/nodemgr"
)

// MultiSourceDownloader handles blob downloads.
type MultiSourceDownloader struct {
	nodeMgr *nodemgr.Manager
}

// DownloadRequest represents a blob download request.
type DownloadRequest struct {
	Name     string
	Digest   string
	Registry string
	Token    string // injected registry token
}

// NewMultiSourceDownloader creates a new downloader.
func NewMultiSourceDownloader(nodeMgr *nodemgr.Manager) *MultiSourceDownloader {
	return &MultiSourceDownloader{nodeMgr: nodeMgr}
}

// Download fetches a blob from the best available node.
func (d *MultiSourceDownloader) Download(ctx context.Context, req DownloadRequest) (io.ReadCloser, int64, int, error) {
	nodes := d.nodeMgr.List()
	var node *nodemgr.Node
	for _, n := range nodes {
		if n.Enabled && n.Healthy {
			for _, t := range n.Targets {
				if t == req.Registry || len(n.Targets) == 0 {
					node = n
					break
				}
			}
			if node != nil {
				break
			}
		}
	}
	if node == nil {
		return nil, 0, 0, fmt.Errorf("no healthy node available")
	}

	blobURL := fmt.Sprintf("%s/v2/%s/blobs/%s", node.URL, req.Name, req.Digest)
	slog.Info("downloading blob", "url", blobURL[:60], "node", node.DisplayName)

	httpReq, err := http.NewRequestWithContext(ctx, "GET", blobURL, nil)
	if err != nil {
		return nil, 0, 0, err
	}

	if req.Token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+req.Token)
	} else if node.Token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+node.Token)
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("download: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, 0, fmt.Errorf("upstream returned %d", resp.StatusCode)
	}

	return resp.Body, resp.ContentLength, 1, nil
}

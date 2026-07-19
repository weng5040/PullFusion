package registry

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/pullfusion/pullfusion/internal/downloader"
)

// serveBlob blob download entry.
func (h *Handler) serveBlob(w http.ResponseWriter, r *http.Request, name, digest, registry string) {
	// HEAD: proxy to first available node
	if r.Method == http.MethodHead {
		h.headBlob(w, r, name, digest, registry)
		return
	}

	// GET: Single-node proxy through docker.1ms.run with token
	slog.Info("blob download", "name", name, "digest", digest[:12])

	dlReq := downloader.DownloadRequest{
		Name:     name,
		Digest:   digest,
		Registry: registry,
	}

	if h.tokenSvc != nil {
		if tok, err := h.tokenSvc.GetToken(r.Context(), registry, name); err == nil && tok != "" {
			dlReq.Token = tok
		}
	}

	body, contentLength, _, err := h.downloader.Download(r.Context(), dlReq)
	if err != nil {
		slog.Error("blob download failed", "name", name, "error", err)
		http.Error(w, "download failed", http.StatusBadGateway)
		return
	}
	defer body.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	if contentLength > 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", contentLength))
	}
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusOK)

	io.Copy(w, body)
}

// headBlob HEAD request handler
func (h *Handler) headBlob(w http.ResponseWriter, r *http.Request, name, digest, registry string) {
	nodes := h.nodeMgr.List()
	var nodeURL string
	for _, n := range nodes {
		if n.Enabled && n.Healthy {
			nodeURL = n.URL
			break
		}
	}
	if nodeURL == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	blobURL := fmt.Sprintf("%s/v2/%s/blobs/%s", nodeURL, name, digest)
	req, err := http.NewRequestWithContext(r.Context(), "HEAD", blobURL, nil)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if h.tokenSvc != nil {
		if tok, err := h.tokenSvc.GetToken(r.Context(), registry, name); err == nil && tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	defer resp.Body.Close()
	transparentHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
}

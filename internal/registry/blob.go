package registry

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/pullfusion/pullfusion/internal/downloader"
)

// serveBlob handles blob download with smart node selection and detailed logging.
func (h *Handler) serveBlob(w http.ResponseWriter, r *http.Request, name, digest, registry string) {
	reqStart := time.Now()
	slog.Info("blob request", "method", r.Method, "name", name, "digest", digest[:19], "registry", registry)

	if r.Method == http.MethodHead {
		h.headBlob(w, r, name, digest, registry)
		slog.Info("head blob done", "name", name, "digest", digest[:19], "duration_ms", time.Since(reqStart).Milliseconds())
		return
	}

	dlReq := downloader.DownloadRequest{
		Name:     name,
		Digest:   digest,
		Registry: registry,
	}

	// Fetch auth token with timing
	tokenStart := time.Now()
	if h.tokenSvc != nil {
		tok, err := h.tokenSvc.GetToken(r.Context(), registry, name)
		if err != nil {
			slog.Warn("blob token fetch failed", "name", name, "error", err, "duration_ms", time.Since(tokenStart).Milliseconds())
		} else if tok != "" {
			dlReq.Token = tok
			slog.Info("blob token acquired", "name", name, "token_len", len(tok), "duration_ms", time.Since(tokenStart).Milliseconds())
		} else {
			slog.Info("blob no token needed", "name", name)
		}
	}

	dlStart := time.Now()
	body, contentLength, nodeCount, err := h.downloader.Download(r.Context(), dlReq)
	dlDuration := time.Since(dlStart)
	totalDuration := time.Since(reqStart)

	if err != nil {
		slog.Error("blob download failed",
			"name", name,
			"digest", digest[:19],
			"error", err,
			"duration_ms", totalDuration.Milliseconds(),
			"dl_duration_ms", dlDuration.Milliseconds(),
		)
		http.Error(w, "download failed", http.StatusBadGateway)
		return
	}
	defer body.Close()

	writeStart := time.Now()
	w.Header().Set("Content-Type", "application/octet-stream")
	if contentLength > 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", contentLength))
	}
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusOK)

	written, copyErr := io.Copy(w, body)
	writeDuration := time.Since(writeStart)

	if copyErr != nil {
		slog.Error("blob write failed", "name", name, "written", written, "error", copyErr)
		return
	}

	slog.Info("blob download complete",
		"name", name,
		"digest", digest[:19],
		"size", contentLength,
		"written", written,
		"nodes", nodeCount,
		"total_ms", totalDuration.Milliseconds(),
		"dl_ms", dlDuration.Milliseconds(),
		"write_ms", writeDuration.Milliseconds(),
	)
}

// headBlob proxies HEAD requests with logging.
func (h *Handler) headBlob(w http.ResponseWriter, r *http.Request, name, digest, registry string) {
	node := h.nodeMgr.SelectBest(registry)
	if node == nil {
		slog.Warn("head blob no healthy node", "name", name, "digest", digest[:19])
		w.WriteHeader(http.StatusNotFound)
		return
	}
	defer h.nodeMgr.ReleaseNode(node, true, 0, 0)

	blobURL := fmt.Sprintf("%s/v2/%s/blobs/%s", node.URL, name, digest)
	slog.Debug("head blob via node", "name", name, "node", node.DisplayName, "url", blobURL[:60])

	req, err := http.NewRequestWithContext(r.Context(), "HEAD", blobURL, nil)
	if err != nil {
		slog.Error("head blob create request", "name", name, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if h.tokenSvc != nil {
		if tok, tokErr := h.tokenSvc.GetToken(r.Context(), registry, name); tokErr == nil && tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("head blob upstream failed", "node", node.DisplayName, "error", err, "duration_ms", time.Since(start).Milliseconds())
		w.WriteHeader(http.StatusNotFound)
		return
	}
	defer resp.Body.Close()

	slog.Info("head blob upstream", "node", node.DisplayName, "status", resp.StatusCode, "size", resp.ContentLength, "duration_ms", time.Since(start).Milliseconds())
	transparentHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
}

// Helper for downloader context - remove if unused by other files
func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

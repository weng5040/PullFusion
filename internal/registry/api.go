package registry

import (
	"log/slog"
	"net/http"
	"regexp"

	"github.com/kspeeder/kspeeder-lite/internal/config"
	"github.com/kspeeder/kspeeder-lite/internal/downloader"
	"github.com/kspeeder/kspeeder-lite/internal/nodemgr"
)

var (
	manifestRe = regexp.MustCompile(`^/v2/(.+)/manifests/([^/]+)$`)
	blobRe     = regexp.MustCompile(`^/v2/(.+)/blobs/([^/]+)$`)
	uploadRe   = regexp.MustCompile(`^/v2/(.+)/blobs/uploads/$`)
)

// Handler 实现 Docker Registry HTTP API V2
type Handler struct {
	cfg        *config.Config
	nodeMgr    *nodemgr.Manager
	downloader *downloader.MultiSourceDownloader
}

// NewHandler 创建 registry handler
func NewHandler(cfg *config.Config, mgr *nodemgr.Manager, dl *downloader.MultiSourceDownloader) *Handler {
	return &Handler{cfg: cfg, nodeMgr: mgr, downloader: dl}
}

// V2Ping GET/HEAD /v2/ — 版本握手
func (h *Handler) V2Ping(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.WriteHeader(http.StatusOK)
}

// ServeHTTP 路由分发（用于 CONNECT 隧道内和 catch-all 路由）
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// /v2/ ping
	if path == "/v2/" {
		h.V2Ping(w, r)
		return
	}

	// /v2/{name}/blobs/uploads/ → 405 Method Not Allowed
	if uploadRe.MatchString(path) {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// /v2/{name}/manifests/{reference}
	if m := manifestRe.FindStringSubmatch(path); m != nil {
		name := m[1]
		reference := m[2]
		slog.Info("manifest request", "name", name, "ref", reference)
		h.proxyManifest(w, r, name, reference)
		return
	}

	// /v2/{name}/blobs/{digest}
	if m := blobRe.FindStringSubmatch(path); m != nil {
		name := m[1]
		digest := m[2]
		slog.Info("blob request", "name", name, "digest", digest)
		h.serveBlob(w, r, name, digest)
		return
	}

	http.NotFound(w, r)
}

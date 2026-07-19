package registry

import (
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/pullfusion/pullfusion/internal/config"
	"github.com/pullfusion/pullfusion/internal/downloader"
	"github.com/pullfusion/pullfusion/internal/auth"
	"github.com/pullfusion/pullfusion/internal/nodemgr"
)

// DownloadRecorder 下载记录器接口（由 admin API 实现）
type DownloadRecorder interface {
	RecordDownload(name string, size int64, nodeCount int, duration time.Duration, err error)
}

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
	tokenSvc   *auth.TokenService
	recorder   DownloadRecorder
}

// NewHandler 创建 registry handler
func NewHandler(cfg *config.Config, mgr *nodemgr.Manager, dl *downloader.MultiSourceDownloader, ts *auth.TokenService) *Handler {
	return &Handler{cfg: cfg, nodeMgr: mgr, downloader: dl, tokenSvc: ts}
}

// SetRecorder 设置下载记录器
func (h *Handler) SetRecorder(r DownloadRecorder) {
	h.recorder = r
}

// V2Ping GET/HEAD /v2/ — 版本握手
func (h *Handler) V2Ping(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.Header().Set("Www-Authenticate", "Bearer realm=\"https://docker.1ms.run/openapi/v1/auth/token\",service=\"docker.1ms.run\"")
	w.WriteHeader(http.StatusUnauthorized)
}

// ServeHTTP 路由分发（用于 CONNECT 隧道内和 catch-all 路由）
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	slog.Debug("registry route", "path", path, "method", r.Method)

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
		reg := DetectRegistry(name)
		slog.Info("manifest request", "name", name, "ref", reference, "registry", reg)
		h.proxyManifest(w, r, name, reference, reg)
		return
	}

	// /v2/{name}/blobs/{digest}
	if m := blobRe.FindStringSubmatch(path); m != nil {
		name := m[1]
		digest := m[2]
		reg := DetectRegistry(name)
		slog.Info("blob request", "name", name, "digest", digest, "registry", reg)
		h.serveBlob(w, r, name, digest, reg)
		return
	}

	slog.Warn("unmatched registry path", "path", path)
	http.NotFound(w, r)
}

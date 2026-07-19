package admin

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/pullfusion/pullfusion/internal/downloader"
	"github.com/pullfusion/pullfusion/internal/fetcher"
	"github.com/pullfusion/pullfusion/internal/nodemgr"
)

// ReloadFunc 配置热加载回调
type ReloadFunc func() error

// DownloadRecord 单条下载记录
type DownloadRecord struct {
	Time        string  `json:"time"`
	Name        string  `json:"name"`
	Size        int64   `json:"size"`
	NodeCount   int     `json:"node_count"`
	SpeedKbps   float64 `json:"speed_kbps"`
	DurationSec float64 `json:"duration_sec"`
	Error       string  `json:"error,omitempty"`
}

// API 管理 API
type API struct {
	nodeMgr   *nodemgr.Manager
	reloader  ReloadFunc
	saveFn    func() error
	startTime time.Time

	dlLogMu sync.Mutex
	dlLog   []DownloadRecord
}

// NewAPI 创建管理 API
func NewAPI(mgr *nodemgr.Manager, saveFn func() error) *API {
	return &API{
		nodeMgr:   mgr,
		startTime: time.Now(),
		dlLog:     make([]DownloadRecord, 0, 50),
	}
}

// NewAPIWithReload 创建带热加载的管理 API
func NewAPIWithReload(mgr *nodemgr.Manager, reloader ReloadFunc) *API {
	return &API{
		nodeMgr:   mgr,
		reloader:  reloader,
		startTime: time.Now(),
		dlLog:     make([]DownloadRecord, 0, 50),
	}
}

// SetReloader 设置热加载回调（延迟注入）
func (a *API) SetReloader(reloader ReloadFunc) {
	a.reloader = reloader
}

// StartTime 返回实例创建时间
func (a *API) StartTime() time.Time {
	return a.startTime
}

// RecordDownload 记录一次下载（线程安全，环形缓冲区最多保留 50 条）
func (a *API) RecordDownload(name string, size int64, nodeCount int, duration time.Duration, err error) {
	a.dlLogMu.Lock()
	defer a.dlLogMu.Unlock()

	rec := DownloadRecord{
		Time:        time.Now().Format("15:04:05"),
		Name:        name,
		Size:        size,
		NodeCount:   nodeCount,
		DurationSec: duration.Seconds(),
	}
	if err != nil {
		rec.Error = err.Error()
	}
	if duration.Seconds() > 0 {
		rec.SpeedKbps = float64(size) / 1024.0 / duration.Seconds()
	}

	a.dlLog = append(a.dlLog, rec)
	if len(a.dlLog) > 50 {
		a.dlLog = a.dlLog[len(a.dlLog)-50:]
	}
}

// ServeDashboard GET / 和 GET /dashboard 仪表盘
func (a *API) ServeDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(DashboardHTML)
}

// ListNodes GET /admin/nodes
func (a *API) ListNodes(w http.ResponseWriter, r *http.Request) {
	nodes := a.nodeMgr.List()
	snapshots := make([]nodemgr.NodeSnapshot, len(nodes))
	for i, n := range nodes {
		snapshots[i] = nodemgr.Snapshot(n)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snapshots)
}

// TestNode POST /admin/nodes/{id}/test
func (a *API) TestNode(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	if nodeID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing node id"})
		return
	}

	nodes := a.nodeMgr.List()
	var target *nodemgr.Node
	for _, n := range nodes {
		if n.URL == nodeID || n.DisplayName == nodeID {
			target = n
			break
		}
	}

	if target == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "node not found"})
		return
	}

	a.nodeMgr.TestSingleNode(target)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "testing",
		"node":   target.URL,
	})
}

// FetchNodes POST /admin/nodes/fetch — 从远程源抓取免费节点
func (a *API) FetchNodes(w http.ResponseWriter, r *http.Request) {
	result, err := fetcher.FetchAndMerge(r.Context(), a.nodeMgr, a.saveFn)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// Stats GET /admin/stats
func (a *API) Stats(w http.ResponseWriter, r *http.Request) {
	total, healthy := a.nodeMgr.GetHealthStatus()
	stats := downloader.GetGlobalStats()
	resp := map[string]interface{}{
		"nodes_total":      total,
		"nodes_healthy":    healthy,
		"active_downloads": stats.Active,
		"downloads_total":  stats.Completed,
		"download_errors":  stats.Failed,
		"error_rate":       stats.ErrorRate,
		"cache_hits":       0,
		"cache_misses":     0,
		"uptime_seconds":   time.Since(a.startTime).Seconds(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// GetDownloads GET /admin/downloads
func (a *API) GetDownloads(w http.ResponseWriter, r *http.Request) {
	a.dlLogMu.Lock()
	defer a.dlLogMu.Unlock()

	// 返回副本（倒序，最新在前）
	result := make([]DownloadRecord, len(a.dlLog))
	for i, rec := range a.dlLog {
		result[len(a.dlLog)-1-i] = rec
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// ReloadConfig POST /admin/config/reload
func (a *API) ReloadConfig(w http.ResponseWriter, r *http.Request) {
	if a.reloader == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{
			"status": "reload not available",
			"error":  "no reload function configured",
		})
		return
	}

	if err := a.reloader(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"status": "failed",
			"error":  err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "config reloaded"})
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

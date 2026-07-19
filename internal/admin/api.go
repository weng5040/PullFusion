package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/pullfusion/pullfusion/internal/fetcher"
	"github.com/pullfusion/pullfusion/internal/nodemgr"
	"github.com/pullfusion/pullfusion/internal/speedtest"
	"github.com/pullfusion/pullfusion/internal/store"
)

// ReloadFunc 配置热加载回调
type ReloadFunc func() error

// DownloadRecord 单条下载记录
type DownloadRecord struct {
	Time       string  `json:"time"`
	Name       string  `json:"name"`
	Size       int64   `json:"size"`
	SpeedKbps  float64 `json:"speed_kbps"`
	DurationSec float64 `json:"duration_sec"`
	Error      bool    `json:"error"`
}

// API 管理 API
type API struct {
	nodeMgr     *nodemgr.Manager
	reloader    ReloadFunc
	saveFn      func() error
	speedTester *speedtest.Tester
	db          *store.DB
	startTime   time.Time

	dlLogMu sync.Mutex
	dlLog   []DownloadRecord
}

// NewAPI 创建管理 API
func NewAPI(mgr *nodemgr.Manager, saveFn func() error) *API {
	return &API{nodeMgr: mgr, saveFn: saveFn, speedTester: speedtest.New(0), startTime: time.Now(), dlLog: make([]DownloadRecord, 0, 50)}
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

// SetReloader 设置热加载回调
func (a *API) SetReloader(fn ReloadFunc) { a.reloader = fn }

// ServeDashboard 返回仪表盘 HTML
func (a *API) ServeDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(DashboardHTML)
}

// ServeDBConsole 返回数据库管理 HTML
func (a *API) ServeDBConsole(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(DBConsoleHTML)
}

// SetDB 设置数据库引用
func (a *API) RecordDownload(name string, size int64, nodeCount int, duration time.Duration, err error) {
	a.dlLogMu.Lock()
	defer a.dlLogMu.Unlock()
	a.dlLog = append(a.dlLog, DownloadRecord{
		Time: time.Now().Format(time.RFC3339),
		Name: name, Size: size, SpeedKbps: float64(size) / duration.Seconds() / 1024,
		DurationSec: duration.Seconds(), Error: err != nil,
	})
	if len(a.dlLog) > 50 { a.dlLog = a.dlLog[len(a.dlLog)-50:] }
}
func (a *API) SetDB(database *store.DB) { a.db = database }

// ListNodes GET /admin/nodes
func (a *API) ListNodes(w http.ResponseWriter, r *http.Request) {
	nodes := a.nodeMgr.GetScoredNodes()
	writeJSON(w, http.StatusOK, nodes)
}

// TestNode POST /admin/nodes/{id}/test
func (a *API) TestNode(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "id")
	a.testNodeByURL(nodeID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// TestOneNode POST /admin/nodes/test-one - test a single node by URL
func (a *API) TestOneNode(w http.ResponseWriter, r *http.Request) {
	var req struct{ URL string `json:"url"` }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing url"})
		return
	}
	a.testNodeByURL(req.URL)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "url": req.URL})
}

// TestAllNodes POST /admin/nodes/test-all - test all enabled nodes
func (a *API) TestAllNodes(w http.ResponseWriter, r *http.Request) {
	go func() {
		for _, n := range a.nodeMgr.List() {
			if !n.Enabled {
				continue
			}
			r := a.speedTester.TestOne(speedtest.NodeInfo{URL: n.URL, Token: n.Token})
			if r.Error == "" {
				a.nodeMgr.RecordDownload(n.URL, r.LatencyMs, r.SpeedKBps, r.Bytes/1024, true)
			} else {
				a.nodeMgr.RecordDownload(n.URL, r.LatencyMs, 0, 0, false)
			}
		}
	}()
	writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
}

func (a *API) testNodeByURL(targetURL string) {
	r := a.speedTester.TestOne(speedtest.NodeInfo{URL: targetURL})
	if r.Error == "" {
		a.nodeMgr.RecordDownload(r.NodeURL, r.LatencyMs, r.SpeedKBps, r.Bytes/1024, true)
	} else {
		a.nodeMgr.RecordDownload(r.NodeURL, r.LatencyMs, 0, 0, false)
	}
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
	total, _ := a.nodeMgr.GetHealthStatus()
	resp := map[string]interface{}{
		"nodes_total":      total,
		"nodes_healthy":    total,
		"active_downloads": 0,
		"downloads_total":  0,
		"download_errors":  0,
		"error_rate":       0,
		"cache_hits":       0,
		"cache_misses":     0,
		"uptime_seconds":   time.Since(a.startTime).Seconds(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// ReloadConfig POST /admin/config/reload
func (a *API) ReloadConfig(w http.ResponseWriter, r *http.Request) {
	if a.reloader == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "reloader not configured"})
		return
	}
	if err := a.reloader(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
}

// ─── DB Console Handlers ──────────────────────────────────────

// DBTables GET /admin/db/tables
func (a *API) DBTables(w http.ResponseWriter, r *http.Request) {
	if a.db == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "no db"})
		return
	}
	tables, _ := a.db.ListTables()
	writeJSON(w, http.StatusOK, tables)
}

// DBSchema GET /admin/db/schema/{table}
func (a *API) DBSchema(w http.ResponseWriter, r *http.Request) {
	if a.db == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "no db"})
		return
	}
	table := chi.URLParam(r, "table")
	schema, _ := a.db.TableSchema(table)
	writeJSON(w, http.StatusOK, schema)
}

// DBData GET /admin/db/data?table=&page=&limit=&col=&val=
func (a *API) DBData(w http.ResponseWriter, r *http.Request) {
	if a.db == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "no db"})
		return
	}
	q := r.URL.Query()
	table := q.Get("table")
	page, _ := strconv.Atoi(q.Get("page"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 50
	}
	cols, rows, total, _ := a.db.GenericQuery(table, q.Get("col"), q.Get("val"), page, limit)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"cols": cols, "rows": rows, "total": total, "page": page, "limit": limit,
	})
}

// DBInsert POST /admin/db/insert
func (a *API) DBInsert(w http.ResponseWriter, r *http.Request) {
	if a.db == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "no db"})
		return
	}
	var req struct {
		Table string            `json:"table"`
		Data  map[string]string `json:"data"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if err := a.db.GenericInsert(req.Table, req.Data); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// DBUpdate POST /admin/db/update
func (a *API) DBUpdate(w http.ResponseWriter, r *http.Request) {
	if a.db == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "no db"})
		return
	}
	var req struct {
		Table string            `json:"table"`
		Data  map[string]string `json:"data"`
		Pk    string            `json:"pk"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if err := a.db.GenericUpdate(req.Table, req.Pk, req.Data); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// DBDelete POST /admin/db/delete
func (a *API) DBDelete(w http.ResponseWriter, r *http.Request) {
	if a.db == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "no db"})
		return
	}
	var req struct {
		Table string `json:"table"`
		Pk    string `json:"pk"`
		Val   string `json:"val"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if err := a.db.GenericDelete(req.Table, req.Pk, req.Val); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ─── Helpers ──────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

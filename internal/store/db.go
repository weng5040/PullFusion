package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct{ db *sql.DB }

// ── Node record ──

type NodeRecord struct {
	ID          int64
	URL         string
	DisplayName string
	Enabled     bool
	Targets     []string
	Token       string
	Tags        []TagEntry
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type TagEntry struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// ── Metric event types ──

const (
	EventLatency = "latency" // event_value = milliseconds
	EventSpeed   = "speed"   // event_value = KB/s
	EventSuccess = "success" // event_value = 1 (ok) or 0 (fail)
	EventBytes   = "bytes"   // event_value = KB downloaded
)

// MetricEvent is one row in the new node_metrics table.
type MetricEvent struct {
	NodeURL    string
	EventType  string
	EventValue int64
}

// ScoreSnapshot is the computed score at a point in time.
type ScoreSnapshot struct {
	NodeURL string
	Score   int32   // 0~10000
	Latency float64 // avg ms in window
	Speed   float64 // avg KB/s in window
	Success float64 // 0.0~1.0
	Bytes   int64   // total KB
}

// ── Stats ──

var (
	Period7Day   = StatsPeriod{"7d", 7 * 86400}
	Period15Day  = StatsPeriod{"15d", 15 * 86400}
	Period30Day  = StatsPeriod{"30d", 30 * 86400}
	Period180Day = StatsPeriod{"180d", 180 * 86400}
	Period365Day = StatsPeriod{"365d", 365 * 86400}
	AllPeriods   = []StatsPeriod{Period7Day, Period15Day, Period30Day, Period180Day, Period365Day}
)

type StatsPeriod struct{ Name string; Seconds int64 }

type AggregatedStats struct {
	NodeURL     string  `json:"node_url"`
	Total       int     `json:"total_requests"`
	Successes   int     `json:"successes"`
	Failures    int     `json:"failures"`
	SuccessRate float64 `json:"success_rate"`
	AvgLatency  float64 `json:"avg_latency_ms"`
	AvgSpeed    float64 `json:"avg_speed_kbps"`
	AvgScore    float64 `json:"avg_score"`
	TotalBytes  int64   `json:"total_bytes"`
	LastUsed    string  `json:"last_used"`
}

// ── Open ──

func Open(dataDir string) (*DB, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	path := filepath.Join(dataDir, "pullfusion.db")
	db, err := sql.Open("sqlite", path+"?_journal=WAL&_busy_timeout=5000&_sync=1")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	d := &DB{db: db}
	if err := d.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	slog.Info("store: database opened", "path", path)
	return d, nil
}

func (d *DB) migrate() error {
	_, err := d.db.Exec(`
		CREATE TABLE IF NOT EXISTS nodes (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			url          TEXT UNIQUE NOT NULL,
			display_name TEXT NOT NULL,
			enabled      INTEGER DEFAULT 1,
			targets      TEXT DEFAULT '["dockerhub"]',
			token        TEXT DEFAULT '',
			tags_name_1  TEXT DEFAULT '',
			tags_color_1 TEXT DEFAULT '',
			tags_name_2  TEXT DEFAULT '',
			tags_color_2 TEXT DEFAULT '',
			tags_name_3  TEXT DEFAULT '',
			tags_color_3 TEXT DEFAULT '',
			tags_name_4  TEXT DEFAULT '',
			tags_color_4 TEXT DEFAULT '',
			tags_name_5  TEXT DEFAULT '',
			tags_color_5 TEXT DEFAULT '',
			created_at   INTEGER NOT NULL,
			updated_at   INTEGER NOT NULL
		);

		CREATE TABLE IF NOT EXISTS node_metrics (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			node_url    TEXT NOT NULL,
			event_type  TEXT NOT NULL,      -- latency / speed / success / bytes
			event_value INTEGER NOT NULL,   -- raw value (no unit)
			timestamp   INTEGER NOT NULL    -- Unix seconds
		);

		CREATE INDEX IF NOT EXISTS idx_metrics_url   ON node_metrics(node_url);
		CREATE INDEX IF NOT EXISTS idx_metrics_time  ON node_metrics(timestamp);
		CREATE INDEX IF NOT EXISTS idx_metrics_etype ON node_metrics(event_type);
		CREATE INDEX IF NOT EXISTS idx_metrics_url_type_time ON node_metrics(node_url, event_type, timestamp);
	`)
	return err
}

// ─── Node CRUD ──────────────────────────────────────────────

func tagValue(tags []TagEntry, idx int, field string) string {
	if idx < len(tags) {
		switch field {
		case "name":
			return tags[idx].Name
		case "color":
			return tags[idx].Color
		}
	}
	return ""
}

func (d *DB) SaveNodes(nodes []NodeRecord) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	for _, n := range nodes {
		targetsJSON, _ := json.Marshal(n.Targets)
		_, err := tx.Exec(`
			INSERT INTO nodes (url, display_name, enabled, targets, token,
				tags_name_1, tags_color_1, tags_name_2, tags_color_2, tags_name_3, tags_color_3,
				tags_name_4, tags_color_4, tags_name_5, tags_color_5, created_at, updated_at)
			VALUES (?,?,?,?,?, ?,?,?,?,?,?, ?,?,?,?, COALESCE((SELECT created_at FROM nodes WHERE url=?),?),?)
			ON CONFLICT(url) DO UPDATE SET
				display_name=excluded.display_name, enabled=excluded.enabled,
				targets=excluded.targets, token=excluded.token,
				tags_name_1=excluded.tags_name_1, tags_color_1=excluded.tags_color_1,
				tags_name_2=excluded.tags_name_2, tags_color_2=excluded.tags_color_2,
				tags_name_3=excluded.tags_name_3, tags_color_3=excluded.tags_color_3,
				tags_name_4=excluded.tags_name_4, tags_color_4=excluded.tags_color_4,
				tags_name_5=excluded.tags_name_5, tags_color_5=excluded.tags_color_5,
				updated_at=excluded.updated_at`,
			n.URL, n.DisplayName, boolToInt(n.Enabled), string(targetsJSON), n.Token,
			tagValue(n.Tags, 0, "name"), tagValue(n.Tags, 0, "color"),
			tagValue(n.Tags, 1, "name"), tagValue(n.Tags, 1, "color"),
			tagValue(n.Tags, 2, "name"), tagValue(n.Tags, 2, "color"),
			tagValue(n.Tags, 3, "name"), tagValue(n.Tags, 3, "color"),
			tagValue(n.Tags, 4, "name"), tagValue(n.Tags, 4, "color"),
			n.URL, now, now,
		)
		if err != nil {
			return fmt.Errorf("save node %s: %w", n.URL, err)
		}
	}
	return tx.Commit()
}

func (d *DB) LoadNodes() ([]NodeRecord, error) {
	rows, err := d.db.Query(`SELECT id, url, display_name, enabled, targets, token,
		tags_name_1, tags_color_1, tags_name_2, tags_color_2, tags_name_3, tags_color_3,
		tags_name_4, tags_color_4, tags_name_5, tags_color_5, created_at, updated_at FROM nodes`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []NodeRecord
	for rows.Next() {
		var n NodeRecord
		var targetsJSON string
		var enabled int
		var ca, ua int64
		var tn1, tc1, tn2, tc2, tn3, tc3, tn4, tc4, tn5, tc5 string
		if err := rows.Scan(&n.ID, &n.URL, &n.DisplayName, &enabled, &targetsJSON, &n.Token,
			&tn1, &tc1, &tn2, &tc2, &tn3, &tc3, &tn4, &tc4, &tn5, &tc5, &ca, &ua); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(targetsJSON), &n.Targets)
		n.Enabled = enabled != 0
		n.CreatedAt = time.Unix(ca, 0)
		n.UpdatedAt = time.Unix(ua, 0)
		if tn1 != "" {
			n.Tags = append(n.Tags, TagEntry{Name: tn1, Color: tc1})
		}
		if tn2 != "" {
			n.Tags = append(n.Tags, TagEntry{Name: tn2, Color: tc2})
		}
		if tn3 != "" {
			n.Tags = append(n.Tags, TagEntry{Name: tn3, Color: tc3})
		}
		if tn4 != "" {
			n.Tags = append(n.Tags, TagEntry{Name: tn4, Color: tc4})
		}
		if tn5 != "" {
			n.Tags = append(n.Tags, TagEntry{Name: tn5, Color: tc5})
		}
		result = append(result, n)
	}
	return result, rows.Err()
}

// ─── Metric insert ──────────────────────────────────────────

func (d *DB) InsertMetricEvent(e MetricEvent) error {
	_, err := d.db.Exec(
		`INSERT INTO node_metrics (node_url, event_type, event_value, timestamp) VALUES (?, ?, ?, ?)`,
		e.NodeURL, e.EventType, e.EventValue, time.Now().Unix(),
	)
	return err
}

// InsertBatch writes all event types for one download in a single tx.
func (d *DB) InsertDownloadEvents(nodeURL string, latencyMs, speedKBps, byteKB int64, success bool) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	successVal := int64(0)
	if success {
		successVal = 1
	}
	exec := func(etype string, val int64) {
		if err != nil {
			return
		}
		_, err = tx.Exec(`INSERT INTO node_metrics (node_url, event_type, event_value, timestamp) VALUES (?,?,?,?)`,
			nodeURL, etype, val, now)
	}
	exec(EventLatency, latencyMs)
	exec(EventSpeed, speedKBps)
	exec(EventSuccess, successVal)
	exec(EventBytes, byteKB)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ─── Scoring queries (window-based from DB) ──────────────────

// ComputeScore computes the current score for a node URL entirely from DB.
func (d *DB) ComputeScore(nodeURL string) ScoreSnapshot {
	var ss ScoreSnapshot
	ss.NodeURL = nodeURL
	now := time.Now().Unix()

	// Latency: avg of last 24h, ignoring 0
	d.db.QueryRow(`SELECT COALESCE(AVG(event_value), 0) FROM node_metrics
		WHERE node_url=? AND event_type=? AND timestamp > ? AND event_value > 0`,
		nodeURL, EventLatency, now-86400).Scan(&ss.Latency)

	// Speed: avg of last 48h, ignoring 0
	d.db.QueryRow(`SELECT COALESCE(AVG(event_value), 0) FROM node_metrics
		WHERE node_url=? AND event_type=? AND timestamp > ? AND event_value > 0`,
		nodeURL, EventSpeed, now-172800).Scan(&ss.Speed)

	// Success: rate in last 7d
	var total, successes float64
	d.db.QueryRow(`SELECT COUNT(*) FROM node_metrics
		WHERE node_url=? AND event_type=? AND timestamp > ?`,
		nodeURL, EventSuccess, now-604800).Scan(&total)
	d.db.QueryRow(`SELECT COUNT(*) FROM node_metrics
		WHERE node_url=? AND event_type=? AND event_value=1 AND timestamp > ?`,
		nodeURL, EventSuccess, now-604800).Scan(&successes)
	if total > 0 {
		ss.Success = successes / total
	}

	// Bytes: sum all
	d.db.QueryRow(`SELECT COALESCE(SUM(event_value), 0) FROM node_metrics
		WHERE node_url=? AND event_type=?`,
		nodeURL, EventBytes).Scan(&ss.Bytes)

	// Fail check: if both latest latency and speed are 0, score = 0
	latZero, spdZero := true, true
	var latestLat, latestSpd int64
	d.db.QueryRow(`SELECT COALESCE(event_value,0) FROM node_metrics
		WHERE node_url=? AND event_type=? ORDER BY id DESC LIMIT 1`,
		nodeURL, EventLatency).Scan(&latestLat)
	d.db.QueryRow(`SELECT COALESCE(event_value,0) FROM node_metrics
		WHERE node_url=? AND event_type=? ORDER BY id DESC LIMIT 1`,
		nodeURL, EventSpeed).Scan(&latestSpd)
	if latestLat > 0 {
		latZero = false
	}
	if latestSpd > 0 {
		spdZero = false
	}

	if latZero && spdZero {
		ss.Score = 0
		return ss
	}

	// Compute weighted score
	latScore := 1.0 - min(ss.Latency/1000.0, 1.0)
	if ss.Latency == 0 {
		latScore = 0.5 // unknown
	}
	spdScore := min(ss.Speed/102400.0, 1.0)
	hlthScore := ss.Success
	loadScore := 1.0 // DB can't track inflight; use success as load proxy

	totalScore := 0.35*latScore + 0.25*spdScore + 0.25*hlthScore + 0.15*loadScore
	ss.Score = int32(totalScore * 10000)
	return ss
}

// ─── Aggregation queries ────────────────────────────────────

func (d *DB) GetStats() (map[string][]AggregatedStats, error) {
	result := make(map[string][]AggregatedStats)
	for _, p := range AllPeriods {
		s, err := d.getStatsForPeriod(p)
		if err != nil {
			return nil, err
		}
		result[p.Name] = s
	}
	return result, nil
}

func (d *DB) getStatsForPeriod(p StatsPeriod) ([]AggregatedStats, error) {
	cutoff := time.Now().Unix() - p.Seconds
	// Group by node_url from latency events (always present) + join success/bytes
	rows, err := d.db.Query(`
		SELECT n.node_url,
			COUNT(*) as total,
			COALESCE(AVG(CASE WHEN n.event_type='latency' THEN n.event_value END), 0),
			COALESCE(AVG(CASE WHEN n.event_type='speed'   THEN n.event_value END), 0),
			COALESCE(SUM(CASE WHEN n.event_type='bytes'   THEN n.event_value END), 0),
			COALESCE(MAX(n.timestamp), 0)
		FROM node_metrics n
		WHERE n.timestamp > ? AND n.event_type IN ('latency','speed','bytes')
		GROUP BY n.node_url
		ORDER BY AVG(CASE WHEN n.event_type='latency' THEN n.event_value END) ASC`,
		cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []AggregatedStats
	for rows.Next() {
		var as AggregatedStats
		var lastUsed int64
		if err := rows.Scan(&as.NodeURL, &as.Total, &as.AvgLatency, &as.AvgSpeed, &as.TotalBytes, &lastUsed); err != nil {
			return nil, err
		}
		// Success rate from success events
		var succTotal, succOK float64
		d.db.QueryRow(`SELECT COUNT(*), SUM(CASE WHEN event_value=1 THEN 1 ELSE 0 END) FROM node_metrics
			WHERE node_url=? AND event_type=? AND timestamp>?`, as.NodeURL, EventSuccess, cutoff).Scan(&succTotal, &succOK)
		as.Successes = int(succOK)
		as.Failures = int(succTotal - succOK)
		if succTotal > 0 {
			as.SuccessRate = succOK / succTotal
		}
		if lastUsed > 0 {
			as.LastUsed = time.Unix(lastUsed, 0).Format(time.RFC3339)
		}
		as.NodeURL = extractShortName(as.NodeURL)
		result = append(result, as)
	}
	return result, rows.Err()
}

// ─── Generic DB access for admin console ─────────────────────

func (d *DB) ListTables() ([]string, error) {
	rows, err := d.db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var name string
		rows.Scan(&name)
		result = append(result, name)
	}
	return result, rows.Err()
}

type ColumnInfo struct {
	Name string
	Type string
	PK   bool
}

func (d *DB) TableSchema(table string) ([]ColumnInfo, error) {
	rows, err := d.db.Query("SELECT name, type, pk FROM pragma_table_info(?)", table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ColumnInfo
	for rows.Next() {
		var ci ColumnInfo
		var pk int
		rows.Scan(&ci.Name, &ci.Type, &pk)
		ci.PK = pk > 0
		result = append(result, ci)
	}
	return result, rows.Err()
}

func (d *DB) GenericQuery(table, searchCol, searchVal string, page, limit int) ([]string, []map[string]interface{}, int, error) {
	schema, err := d.TableSchema(table)
	if err != nil {
		return nil, nil, 0, err
	}
	var cols []string
	for _, c := range schema {
		cols = append(cols, c.Name)
	}

	var total int
	d.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&total)

	var query string
	var args []interface{}
	if searchCol != "" && searchVal != "" {
		query = "SELECT * FROM " + table + " WHERE CAST(" + searchCol + " AS TEXT) LIKE ? ORDER BY rowid DESC LIMIT ? OFFSET ?"
		args = append(args, "%"+searchVal+"%", limit, (page-1)*limit)
	} else {
		query = "SELECT * FROM " + table + " ORDER BY rowid DESC LIMIT ? OFFSET ?"
		args = append(args, limit, (page-1)*limit)
	}

	r, err := d.db.Query(query, args...)
	if err != nil {
		return nil, nil, 0, err
	}
	defer r.Close()

	var rows []map[string]interface{}
	for r.Next() {
		values := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		r.Scan(ptrs...)
		row := make(map[string]interface{})
		for i, c := range cols {
			row[c] = values[i]
		}
		rows = append(rows, row)
	}
	return cols, rows, total, r.Err()
}

func (d *DB) GenericInsert(table string, data map[string]string) error {
	cols := make([]string, 0, len(data))
	vals := make([]interface{}, 0, len(data))
	for k, v := range data {
		cols = append(cols, k)
		vals = append(vals, v)
	}
	ph := make([]string, len(cols))
	for i := range ph {
		ph[i] = "?"
	}
	_, err := d.db.Exec("INSERT INTO "+table+" ("+strings.Join(cols, ",")+") VALUES ("+strings.Join(ph, ",")+")", vals...)
	return err
}

func (d *DB) GenericUpdate(table, pk string, data map[string]string) error {
	sets := make([]string, 0, len(data)-1)
	var vals []interface{}
	var pkVal interface{}
	for k, v := range data {
		if k == pk {
			pkVal = v
			continue
		}
		sets = append(sets, k+" = ?")
		vals = append(vals, v)
	}
	vals = append(vals, pkVal)
	_, err := d.db.Exec("UPDATE "+table+" SET "+strings.Join(sets, ",")+" WHERE "+pk+" = ?", vals...)
	return err
}

func (d *DB) GenericDelete(table, pk, val string) error {
	_, err := d.db.Exec("DELETE FROM "+table+" WHERE "+pk+" = ?", val)
	return err
}

func (d *DB) Close() error { return d.db.Close() }

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func extractShortName(url string) string {
	s := strings.TrimPrefix(url, "https://")
	s = strings.TrimPrefix(s, "http://")
	if idx := strings.Index(s, "/"); idx > 0 {
		s = s[:idx]
	}
	return s
}

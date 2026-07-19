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

// TagEntry represents a single tag with name and color.
type TagEntry struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// NodeRecord is a persisted node entry.
type NodeRecord struct {
	ID          int64
	URL         string
	DisplayName string
	Enabled     bool
	Targets     []string
	Token       string
	Tags        []TagEntry // flattened to tags_name_N / tags_color_N columns
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type MetricRecord struct {
	NodeURL    string
	Timestamp  time.Time
	LatencyMs  int64
	SpeedKBps  int64
	Success    bool
	FailCount  int32
	Score      int32
	InFlight   int32
	Healthy    bool
	BytesTotal int64
}

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
	s := &DB{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	slog.Info("store: database opened", "path", path)
	return s, nil
}

func (s *DB) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS nodes (
			id           INTEGER PRIMARY KEY AUTOINCREMENT, -- 自增主键
			url          TEXT UNIQUE NOT NULL,               -- 镜像站地址
			display_name TEXT NOT NULL,                      -- 展示名称（来自 status.anye.xyz）
			enabled      INTEGER DEFAULT 1,                  -- 是否启用：1=启用 0=禁用
			targets      TEXT DEFAULT '["dockerhub"]',       -- 支持的 registry（JSON 数组）
			token        TEXT DEFAULT '',                    -- 认证 token（预留）
			tags_name_1  TEXT DEFAULT '',                    -- 标签1名称
			tags_color_1 TEXT DEFAULT '',                    -- 标签1颜色
			tags_name_2  TEXT DEFAULT '',                    -- 标签2名称
			tags_color_2 TEXT DEFAULT '',                    -- 标签2颜色
			tags_name_3  TEXT DEFAULT '',                    -- 标签3名称
			tags_color_3 TEXT DEFAULT '',                    -- 标签3颜色
			tags_name_4  TEXT DEFAULT '',                    -- 标签4名称
			tags_color_4 TEXT DEFAULT '',                    -- 标签4颜色
			tags_name_5  TEXT DEFAULT '',                    -- 标签5名称
			tags_color_5 TEXT DEFAULT '',                    -- 标签5颜色
			created_at   INTEGER NOT NULL,                   -- 首次入库时间（Unix 秒）
			updated_at   INTEGER NOT NULL                    -- 最后更新时间（Unix 秒）
		);

		CREATE TABLE IF NOT EXISTS node_metrics (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			node_url    TEXT NOT NULL REFERENCES nodes(url),
			timestamp   INTEGER NOT NULL,
			latency_ms  INTEGER DEFAULT 0,
			speed_kbps  INTEGER DEFAULT 0,
			success     INTEGER DEFAULT 0,
			fail_count  INTEGER DEFAULT 0,
			score       INTEGER DEFAULT 0,
			inflight    INTEGER DEFAULT 0,
			healthy     INTEGER DEFAULT 1,
			bytes_total INTEGER DEFAULT 0
		);

		CREATE INDEX IF NOT EXISTS idx_metrics_url      ON node_metrics(node_url);
		CREATE INDEX IF NOT EXISTS idx_metrics_time     ON node_metrics(timestamp);
		CREATE INDEX IF NOT EXISTS idx_metrics_url_time ON node_metrics(node_url, timestamp);
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

func (s *DB) SaveNodes(nodes []NodeRecord) error {
	tx, err := s.db.Begin()
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
				tags_name_4, tags_color_4, tags_name_5, tags_color_5,
				created_at, updated_at)
			VALUES (?, ?, ?, ?, ?,
				?, ?, ?, ?, ?, ?,
				?, ?, ?, ?,
				COALESCE((SELECT created_at FROM nodes WHERE url=?), ?), ?)
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

func (s *DB) LoadNodes() ([]NodeRecord, error) {
	rows, err := s.db.Query(`SELECT id, url, display_name, enabled, targets, token,
		tags_name_1, tags_color_1, tags_name_2, tags_color_2, tags_name_3, tags_color_3,
		tags_name_4, tags_color_4, tags_name_5, tags_color_5,
		created_at, updated_at FROM nodes`)
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
		// Rebuild tags from columns
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

// ─── Metrics + Aggregation ──────────────────────────────────

func (s *DB) InsertMetric(r MetricRecord) error {
	_, err := s.db.Exec(`
		INSERT INTO node_metrics (node_url, timestamp, latency_ms, speed_kbps, success, fail_count, score, inflight, healthy, bytes_total)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.NodeURL, r.Timestamp.Unix(), r.LatencyMs, r.SpeedKBps,
		boolToInt(r.Success), r.FailCount, r.Score, r.InFlight, boolToInt(r.Healthy), r.BytesTotal)
	return err
}

func (s *DB) LoadLatestMetrics() (map[string]MetricRecord, error) {
	rows, err := s.db.Query(`
		SELECT node_url, timestamp, latency_ms, speed_kbps, success, fail_count, score, inflight, healthy, bytes_total
		FROM node_metrics WHERE id IN (SELECT MAX(id) FROM node_metrics GROUP BY node_url)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]MetricRecord)
	for rows.Next() {
		var r MetricRecord
		var ts int64
		var success, healthy int
		if err := rows.Scan(&r.NodeURL, &ts, &r.LatencyMs, &r.SpeedKBps,
			&success, &r.FailCount, &r.Score, &r.InFlight, &healthy, &r.BytesTotal); err != nil {
			return nil, err
		}
		r.Timestamp = time.Unix(ts, 0)
		r.Success = success != 0
		r.Healthy = healthy != 0
		result[r.NodeURL] = r
	}
	return result, rows.Err()
}

func (s *DB) GetStats() (map[string][]AggregatedStats, error) {
	result := make(map[string][]AggregatedStats)
	for _, p := range AllPeriods {
		stats, err := s.getStatsForPeriod(p)
		if err != nil {
			return nil, err
		}
		result[p.Name] = stats
	}
	return result, nil
}

func (s *DB) GetStatsForPeriod(key string) ([]AggregatedStats, error) {
	for _, p := range AllPeriods {
		if p.Name == key {
			return s.getStatsForPeriod(p)
		}
	}
	return nil, fmt.Errorf("unknown period: %s", key)
}

func (s *DB) getStatsForPeriod(p StatsPeriod) ([]AggregatedStats, error) {
	rows, err := s.db.Query(`
		SELECT node_url, COUNT(*), SUM(CASE WHEN success THEN 1 ELSE 0 END),
			COALESCE(AVG(latency_ms),0), COALESCE(AVG(speed_kbps),0),
			COALESCE(AVG(score),0), COALESCE(SUM(bytes_total),0), COALESCE(MAX(timestamp),0)
		FROM node_metrics WHERE timestamp > ? GROUP BY node_url ORDER BY AVG(score) DESC`,
		time.Now().Unix()-p.Seconds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []AggregatedStats
	for rows.Next() {
		var as AggregatedStats
		var lastUsed int64
		if err := rows.Scan(&as.NodeURL, &as.Total, &as.Successes,
			&as.AvgLatency, &as.AvgSpeed, &as.AvgScore, &as.TotalBytes, &lastUsed); err != nil {
			return nil, err
		}
		as.Failures = as.Total - as.Successes
		if as.Total > 0 {
			as.SuccessRate = float64(as.Successes) / float64(as.Total)
		}
		if lastUsed > 0 {
			as.LastUsed = time.Unix(lastUsed, 0).Format(time.RFC3339)
		}
		as.NodeURL = extractShortName(as.NodeURL)
		result = append(result, as)
	}
	return result, rows.Err()
}

func (s *DB) PruneMetrics(retentionDays int) error {
	cutoff := time.Now().AddDate(0, 0, -retentionDays).Unix()
	_, err := s.db.Exec(`DELETE FROM node_metrics WHERE timestamp < ?`, cutoff)
	if err == nil {
		slog.Info("store: pruned old metrics", "cutoff_days", retentionDays)
	}
	return err
}

func (s *DB) Close() error { return s.db.Close() }

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

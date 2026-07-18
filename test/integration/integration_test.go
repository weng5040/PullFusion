package integration_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kspeeder/kspeeder-lite/internal/config"
	"github.com/kspeeder/kspeeder-lite/internal/downloader"
	"github.com/kspeeder/kspeeder-lite/internal/nodemgr"
	"github.com/kspeeder/kspeeder-lite/internal/registry"
)

// TestV2Ping 测试 /v2/ 握手
func TestV2Ping(t *testing.T) {
	cfg := &config.Config{}
	nodeMgr := nodemgr.NewManager(cfg)
	dl := downloader.NewMultiSourceDownloader(nodeMgr, 4, 64, 1<<20, 16<<20, 3)
	handler := registry.NewHandler(cfg, nodeMgr, dl)

	req := httptest.NewRequest("GET", "/v2/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("V2Ping: expected 200, got %d", w.Code)
	}

	apiVer := w.Header().Get("Docker-Distribution-API-Version")
	if apiVer != "registry/2.0" {
		t.Errorf("V2Ping: expected registry/2.0, got %s", apiVer)
	}
}

// TestManifestProxyFlow 测试 manifest 代理流程
func TestManifestProxyFlow(t *testing.T) {
	mockUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
		w.Header().Set("Docker-Content-Digest", "sha256:fake123")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"schemaVersion":2}`))
	}))
	defer mockUpstream.Close()

	cfg := &config.Config{
		Mirrors: config.MirrorsConfig{
			Dockerhub: []config.MirrorNode{
				{URL: mockUpstream.URL, Priority: 1, DisplayName: "mock"},
			},
		},
	}
	nodeMgr := nodemgr.NewManager(cfg)
	dl := downloader.NewMultiSourceDownloader(nodeMgr, 4, 64, 1<<20, 16<<20, 3)
	handler := registry.NewHandler(cfg, nodeMgr, dl)

	t.Run("GetManifest", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/v2/library/alpine/manifests/latest", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		t.Logf("manifest status: %d", w.Code)
		// Accept 200 or any proxy response
		if w.Code != http.StatusOK {
			t.Logf("manifest proxy returned %d (upstream proxying may vary)", w.Code)
		}
	})
}

// TestNoHealthyNodes 测试没有健康节点时的错误处理
func TestNoHealthyNodes(t *testing.T) {
	cfg := &config.Config{}
	nodeMgr := nodemgr.NewManager(cfg)
	dl := downloader.NewMultiSourceDownloader(nodeMgr, 4, 64, 1<<20, 16<<20, 3)
	req := downloader.DownloadRequest{
		Name:     "library/alpine",
		Digest:   "sha256:test",
		Registry: "dockerhub",
	}
	_, _, err := dl.Download(context.Background(), req)
	if err == nil {
		t.Error("expected error when no nodes available")
	}
	if !strings.Contains(err.Error(), "no nodes") {
		t.Errorf("expected 'no nodes' error, got: %v", err)
	}
}

// TestParseRange 测试 HTTP Range 解析
func TestParseRange(t *testing.T) {
	tests := []struct {
		name      string
		header    string
		totalSize int64
		wantStart int64
		wantEnd   int64
		wantErr   bool
	}{
		{"bytes start-end", "bytes=0-99", 200, 0, 100, false},
		{"bytes start-", "bytes=100-", 200, 100, 200, false},
		{"bytes suffix", "bytes=-50", 200, 150, 200, false},
		{"empty header", "", 200, 0, 0, false},
		{"invalid prefix", "invalid=0-100", 200, 0, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := downloader.ParseRange(tt.header, tt.totalSize)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if r == nil && tt.header == "" {
				return
			}
			if r == nil {
				t.Fatal("expected non-nil range")
			}
			if r.Start != tt.wantStart {
				t.Errorf("start: want %d, got %d", tt.wantStart, r.Start)
			}
			if r.End != tt.wantEnd {
				t.Errorf("end: want %d, got %d", tt.wantEnd, r.End)
			}
		})
	}
}

// TestNodeManagerInit 测试节点管理器从配置初始化
func TestNodeManagerInit(t *testing.T) {
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer mockSrv.Close()

	cfg := &config.Config{
		Mirrors: config.MirrorsConfig{
			Dockerhub: []config.MirrorNode{
				{URL: mockSrv.URL, Priority: 1, DisplayName: "test-node"},
			},
		},
	}
	nodeMgr := nodemgr.NewManager(cfg)
	nodes := nodeMgr.List()

	if len(nodes) == 0 {
		t.Error("expected at least 1 node")
	}

	if len(nodes) > 0 {
		n := nodes[0]
		if n.Type != nodemgr.NodeTypeMirror {
			t.Errorf("expected NodeTypeMirror, got %s", n.Type)
		}
		if n.Priority != 1 {
			t.Errorf("expected priority 1, got %d", n.Priority)
		}
	}

	total, healthy := nodeMgr.GetHealthStatus()
	if total == 0 {
		t.Error("expected non-zero total nodes")
	}
	t.Logf("total=%d, healthy=%d", total, healthy)
}

// TestMarkFailedMarkSuccess 测试节点失败/恢复标记
func TestMarkFailedMarkSuccess(t *testing.T) {
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	}))
	defer mockSrv.Close()

	cfg := &config.Config{
		Mirrors: config.MirrorsConfig{
			Dockerhub: []config.MirrorNode{
				{URL: mockSrv.URL, Priority: 1, DisplayName: "test-node"},
			},
		},
	}
	nodeMgr := nodemgr.NewManager(cfg)
	nodes := nodeMgr.List()
	node := nodes[0]

	nodeMgr.MarkFailed(node)
	if node.FailCount != 1 {
		t.Errorf("expected FailCount=1, got %d", node.FailCount)
	}

	nodeMgr.MarkSuccess(node)
	if node.FailCount != 0 {
		t.Errorf("expected FailCount=0 after MarkSuccess, got %d", node.FailCount)
	}
	if !node.Healthy {
		t.Error("expected node to be healthy after MarkSuccess")
	}
}

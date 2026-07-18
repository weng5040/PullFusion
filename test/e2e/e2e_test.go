//go:build e2e
// +build e2e

package e2e_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	registryPort = 5443
	proxyPort    = 5003
)

// TestDockerPull 完整 docker pull 流程测试
// 需要 Docker daemon 和 kspeeder-lite 服务运行中
func TestDockerPull(t *testing.T) {
	t.Skip("e2e test requires Docker daemon and running kspeeder-lite service")

	// 检查 docker 可用性
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found, skipping e2e test")
	}

	// 检查 kspeeder-lite 是否运行
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	resp, err := client.Get(fmt.Sprintf("http://localhost:%d/healthz", proxyPort))
	if err != nil {
		t.Skipf("kspeeder-lite not running on port %d: %v", proxyPort, err)
	}
	resp.Body.Close()

	t.Log("kspeeder-lite is running, performing docker pull test")

	// 清理旧镜像
	exec.Command("docker", "rmi", "-f", "alpine:latest").Run()

	// 通过 Docker daemon 配置 registry mirror 进行拉取
	// 注意：实际使用时需配置 Docker daemon.json 的 registry-mirrors
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker", "pull", "alpine:latest")
	output, err := cmd.CombinedOutput()
	t.Logf("docker pull output:\n%s", string(output))

	if err != nil {
		t.Errorf("docker pull failed: %v", err)
	}
}

// TestRegistryAPIEndpoints 测试 registry API 端点可达性
func TestRegistryAPIEndpoints(t *testing.T) {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	endpoints := []struct {
		method string
		url    string
		desc   string
	}{
		{"GET", fmt.Sprintf("http://localhost:%d/healthz", proxyPort), "health check"},
		{"GET", fmt.Sprintf("http://localhost:%d/admin/stats", proxyPort), "admin stats"},
		{"GET", fmt.Sprintf("http://localhost:%d/admin/nodes", proxyPort), "admin nodes"},
		{"GET", fmt.Sprintf("http://localhost:%d/metrics", proxyPort), "prometheus metrics"},
	}

	for _, ep := range endpoints {
		t.Run(ep.desc, func(t *testing.T) {
			req, err := http.NewRequest(ep.method, ep.url, nil)
			if err != nil {
				t.Skipf("cannot create request: %v", err)
			}

			resp, err := client.Do(req)
			if err != nil {
				t.Skipf("endpoint %s not reachable: %v", ep.url, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				t.Logf("response body: %s", string(body))
				t.Errorf("%s: expected 200, got %d", ep.desc, resp.StatusCode)
			}
		})
	}
}

// TestMetricsEndpoint 测试 Prometheus metrics 端点格式
func TestMetricsEndpoint(t *testing.T) {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Get(fmt.Sprintf("http://localhost:%d/metrics", proxyPort))
	if err != nil {
		t.Skipf("metrics endpoint not reachable: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read metrics: %v", err)
	}

	metricsText := string(body)

	// 验证必需的 metrics 存在
	requiredMetrics := []string{
		"kspeeder_blob_downloads_total",
		"kspeeder_blob_download_duration_seconds",
		"kspeeder_blob_download_bytes",
		"kspeeder_node_speed_mbps",
		"kspeeder_node_health",
		"kspeeder_node_inflight",
		"kspeeder_active_downloads",
		"kspeeder_config_reloads_total",
	}

	for _, m := range requiredMetrics {
		if !strings.Contains(metricsText, m) {
			t.Errorf("missing metric: %s", m)
		}
	}

	t.Logf("all required metrics found in %d bytes of metrics output", len(metricsText))
}

// TestBuildAndRun 测试编译和启动
func TestBuildAndRun(t *testing.T) {
	t.Skip("e2e test: requires building and running the service")

	// 构建
	buildCmd := exec.Command("go", "build", "-o", "bin/kspeeder-lite", "./cmd/kspeeder-lite/")
	buildCmd.Dir = "../.."
	if output, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, output)
	}

	// 启动服务
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	runCmd := exec.CommandContext(ctx, "./bin/kspeeder-lite", "--config", "configs/nodes.sample.yaml")
	runCmd.Dir = "../.."

	if err := runCmd.Start(); err != nil {
		t.Fatalf("failed to start kspeeder-lite: %v", err)
	}

	// 等待启动
	time.Sleep(3 * time.Second)

	// 验证健康检查
	resp, err := http.Get("http://localhost:5003/healthz")
	if err != nil {
		runCmd.Process.Kill()
		t.Fatalf("health check failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		runCmd.Process.Kill()
		t.Fatalf("health check returned %d", resp.StatusCode)
	}

	// 清理
	runCmd.Process.Signal(os.Interrupt)
	runCmd.Wait()
}

// TestConfigReload 测试配置热加载
func TestConfigReload(t *testing.T) {
	t.Skip("e2e test: requires modifying config file")

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Post(
		fmt.Sprintf("http://localhost:%d/admin/config/reload", proxyPort),
		"application/json",
		strings.NewReader("{}"),
	)
	if err != nil {
		t.Skipf("config reload endpoint not reachable: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	t.Logf("config reload response: %s", string(body))
}

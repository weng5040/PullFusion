package server

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/pullfusion/pullfusion/internal/admin"
	"github.com/pullfusion/pullfusion/internal/config"
	"github.com/pullfusion/pullfusion/internal/metrics"
	"github.com/pullfusion/pullfusion/internal/registry"
)

// NewConnectProxy 创建 CONNECT 代理服务器
func NewConnectProxy(cfg *config.Config, deps *Dependencies) *http.Server {
	r := chi.NewRouter()
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)

	// Prometheus 指标
	r.Handle("/metrics", metrics.Handler())

	// 管理 API
	adminAPI := admin.NewAPI(deps.NodeMgr)
	r.Get("/healthz", healthzHandler(deps))
	r.Get("/dashboard", adminAPI.ServeDashboard)
	r.Get("/", adminAPI.ServeDashboard)
	r.Get("/admin/nodes", adminAPI.ListNodes)
	r.Post("/admin/nodes/{id}/test", adminAPI.TestNode)
	r.Post("/admin/nodes/fetch", adminAPI.FetchNodes)
	r.Get("/admin/stats", adminAPI.Stats)
	r.Post("/admin/config/reload", adminAPI.ReloadConfig)

	// CONNECT 代理处理器
	regHandler := registry.NewHandler(cfg, deps.NodeMgr, deps.Downloader, deps.TokenService)
	proxyHandler := &connectHandler{
		cfg:        cfg,
		regHandler: regHandler,
	}

	r.HandleFunc("/*", proxyHandler.ServeHTTP)

	return &http.Server{
		Addr:    ":5003",
		Handler: r,
	}
}

// connectHandler CONNECT 代理处理器
type connectHandler struct {
	cfg        *config.Config
	regHandler *registry.Handler
}

// ServeHTTP 处理 CONNECT 请求
func (h *connectHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		h.handleConnect(w, r)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

// handleConnect 处理 CONNECT 隧道
func (h *connectHandler) handleConnect(w http.ResponseWriter, r *http.Request) {
	// Basic Auth 校验
	if h.cfg.Server.ProxyAuth != "" {
		if !h.checkAuth(r) {
			w.Header().Set("Proxy-Authenticate", "Basic")
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
	}

	// 解析目标地址
	_, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	slog.Info("CONNECT request", "host", r.Host)
	// 任意 registry 域名的 CONNECT 都走内部 handler
	if port == "443" {
		h.handleRegistryTunnel(w, r)
		return
	}

}
// handleRegistryTunnel 处理 registry 内部隧道
// 从 clientConn 读取 HTTP 请求，通过 registry handler 路由，写回响应
func (h *connectHandler) handleRegistryTunnel(w http.ResponseWriter, r *http.Request) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		slog.Error("hijack failed", "error", err)
		return
	}
	defer clientConn.Close()

	// 发送 200 Connection Established
	clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	// 使用 bufio.Reader 读取隧道内的 HTTP 请求
	reader := bufio.NewReader(clientConn)

	// 为整个隧道创建可取消的 context，隧道关闭时自动取消所有进行中的请求
	tunnelCtx, tunnelCancel := context.WithCancel(context.Background())
	defer tunnelCancel()

	for {
		// 使用 http.ReadRequest 读取完整的 HTTP 请求
		req, err := http.ReadRequest(reader)
		if err != nil {
			if err != io.EOF {
				slog.Error("read tunnel request", "error", err)
			}
			return
		}
		// 确保 URL 不为 nil（http.ReadRequest 可能返回 nil URL 对于代理请求）
		if req.URL == nil {
			req.URL = &url.URL{}
		}

		// 将请求绑定到隧道 context，确保隧道关闭时触发的下载能取消
		req = req.WithContext(tunnelCtx)

		// 创建 ResponseWriter 包装 clientConn（流式写入）
		respWriter := &tunnelResponseWriter{
			conn:   clientConn,
			header: make(http.Header),
		}

		// 路由请求到 registry handler
		h.regHandler.ServeHTTP(respWriter, req)

		// 如果是非持久连接，关闭
		if strings.EqualFold(req.Header.Get("Connection"), "close") {
			return
		}

		// 对于无 body 的请求（GET/HEAD），http.ReadRequest 不会消费多余字节
		// 但如果 handler 没有完全消费 body，需要 drain
		if req.Body != nil {
			io.Copy(io.Discard, req.Body)
			req.Body.Close()
		}
	}
}

// tunnelResponseWriter 隧道内流式响应写入器
// 将 handler 产生的 HTTP 响应直接写入 clientConn
type tunnelResponseWriter struct {
	conn        net.Conn
	header      http.Header
	wroteHeader bool
	statusCode  int
}

func (w *tunnelResponseWriter) Header() http.Header {
	return w.header
}

func (w *tunnelResponseWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	// 直接流式写入连接，不缓冲
	return w.conn.Write(data)
}

func (w *tunnelResponseWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.statusCode = statusCode

	// 构建并写入 HTTP 响应头
	statusText := http.StatusText(statusCode)
	if statusText == "" {
		statusText = "Unknown"
	}

	var resp strings.Builder
	fmt.Fprintf(&resp, "HTTP/1.1 %d %s\r\n", statusCode, statusText)

	for key, vals := range w.header {
		for _, val := range vals {
			fmt.Fprintf(&resp, "%s: %s\r\n", key, val)
		}
	}
	resp.WriteString("\r\n")

	w.conn.Write([]byte(resp.String()))
}

// checkAuth 校验 Basic Auth
func (h *connectHandler) checkAuth(r *http.Request) bool {
	auth := r.Header.Get("Proxy-Authorization")
	if auth == "" {
		return false
	}
	const prefix = "Basic "
	if !strings.HasPrefix(auth, prefix) {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(auth[len(prefix):])
	if err != nil {
		return false
	}
	return string(decoded) == h.cfg.Server.ProxyAuth
}

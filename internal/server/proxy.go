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
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/pullfusion/pullfusion/internal/admin"
	"github.com/pullfusion/pullfusion/internal/config"
	"github.com/pullfusion/pullfusion/internal/metrics"
	"github.com/pullfusion/pullfusion/internal/registry"
)

func NewConnectProxy(cfg *config.Config, deps *Dependencies) *http.Server {
	r := chi.NewRouter()
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)

	r.Handle("/metrics", metrics.Handler())

	adminAPI := admin.NewAPI(deps.NodeMgr, nil)
	r.Get("/healthz", healthzHandler(deps))
	r.Get("/dashboard", adminAPI.ServeDashboard)
	r.Get("/", adminAPI.ServeDashboard)
	r.Get("/admin/nodes", adminAPI.ListNodes)
	r.Post("/admin/nodes/{id}/test", adminAPI.TestNode)
	r.Get("/admin/stats", adminAPI.Stats)
	r.Post("/admin/config/reload", adminAPI.ReloadConfig)
	r.Post("/admin/nodes/fetch", adminAPI.FetchNodes)

	regHandler := registry.NewHandler(cfg, deps.NodeMgr, deps.Downloader, deps.TokenService)
	proxyHandler := &connectHandler{cfg: cfg, regHandler: regHandler}

	r.HandleFunc("/*", proxyHandler.ServeHTTP)

	return &http.Server{Addr: ":5003", Handler: r}
}

type connectHandler struct {
	cfg        *config.Config
	regHandler *registry.Handler
}

func (h *connectHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		h.handleConnect(w, r)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

var registryDomains = map[string]bool{
	"registry-1.docker.io": true,
	
	
	
	
}

func (h *connectHandler) handleConnect(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Server.ProxyAuth != "" && !h.checkAuth(r) {
		w.Header().Set("Proxy-Authenticate", "Basic")
		w.WriteHeader(http.StatusProxyAuthRequired)
		return
	}

	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	slog.Info("CONNECT request", "host", r.Host)

	// Registry hosts: handle internally with multi-source acceleration
	if port == "443" && registryDomains[host] {
		h.handleRegistryTunnel(w, r)
		return
	}

	// All other hosts (auth.docker.io, production.cloudflare.docker.com, etc.): transparent TCP tunnel
	h.handleTransparentTunnel(w, r, host, port)
}

func (h *connectHandler) handleTransparentTunnel(w http.ResponseWriter, r *http.Request, host, port string) {
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

	target := host + ":" + port
	targetConn, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		slog.Error("transparent tunnel dial failed", "target", target, "error", err)
		clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}
	defer targetConn.Close()

	clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	done := make(chan struct{}, 2)
	go func() { io.Copy(targetConn, clientConn); done <- struct{}{} }()
	go func() { io.Copy(clientConn, targetConn); done <- struct{}{} }()
	<-done
}

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

	clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	reader := bufio.NewReader(clientConn)
	tunnelCtx, tunnelCancel := context.WithCancel(context.Background())
	defer tunnelCancel()

	for {
		req, err := http.ReadRequest(reader)
		if err != nil {
			if err != io.EOF {
				slog.Error("read tunnel request", "error", err)
			}
			return
		}
		if req.URL == nil {
			req.URL = &url.URL{}
		}
		req = req.WithContext(tunnelCtx)

		respWriter := &tunnelResponseWriter{conn: clientConn, header: make(http.Header)}
		h.regHandler.ServeHTTP(respWriter, req)

		if strings.EqualFold(req.Header.Get("Connection"), "close") {
			return
		}
		if req.Body != nil {
			io.Copy(io.Discard, req.Body)
			req.Body.Close()
		}
	}
}

type tunnelResponseWriter struct {
	conn        net.Conn
	header      http.Header
	wroteHeader bool
	statusCode  int
}

func (w *tunnelResponseWriter) Header() http.Header { return w.header }
func (w *tunnelResponseWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.conn.Write(data)
}
func (w *tunnelResponseWriter) WriteHeader(statusCode int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.statusCode = statusCode

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

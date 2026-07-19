package server

import (
	"crypto/tls"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/pullfusion/pullfusion/internal/auth"
	"github.com/pullfusion/pullfusion/internal/config"
	"github.com/pullfusion/pullfusion/internal/downloader"
	"github.com/pullfusion/pullfusion/internal/nodemgr"
	"github.com/pullfusion/pullfusion/internal/registry"
	"github.com/pullfusion/pullfusion/internal/tlsutil"
)

// Dependencies 服务器依赖
type Dependencies struct {
	Config     *config.Config
	NodeMgr    *nodemgr.Manager
	Downloader   *downloader.MultiSourceDownloader
	TokenService *auth.TokenService
	// Recorder 下载日志记录器（admin API 实现 DownloadRecorder 接口）
	Recorder registry.DownloadRecorder
}

// NewRegistryServer 创建 registry HTTPS 服务器
func NewRegistryServer(cfg *config.Config, deps *Dependencies) (*http.Server, error) {
	r := chi.NewRouter()
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)
	r.Use(chimw.RealIP)

	// Registry API V2 路由 — 使用中间件拦截所有 /v2/ 路径
	regHandler := registry.NewHandler(cfg, deps.NodeMgr, deps.Downloader, deps.TokenService)
	if deps.Recorder != nil {
		regHandler.SetRecorder(deps.Recorder)
	}

	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/v2/") {
				regHandler.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	})

	// 健康检查
	r.Get("/healthz", healthzHandler(deps))

	// TLS 证书处理
	certFile := "data/cert.pem"
	if _, err := os.Stat("data"); os.IsNotExist(err) { certFile = "/opt/pullfusion/data/cert.pem" }
	keyFile := "data/key.pem"

	var tlsConfig *tls.Config

	// 1. Try configured cert first
	if cfg.Server.TLS.Cert != "" && cfg.Server.TLS.Key != "" {
		cert, err := tls.LoadX509KeyPair(cfg.Server.TLS.Cert, cfg.Server.TLS.Key)
		if err == nil {
			tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
			slog.Info("using configured TLS cert", "cert", cfg.Server.TLS.Cert)
		}
	}

	// 2. Try saved self-signed cert from previous run
	if tlsConfig == nil {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err == nil {
			tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
			slog.Info("using saved self-signed cert")
		}
	}

	// 3. Generate new self-signed cert, save to disk
	if tlsConfig == nil {
		cert, err := tlsutil.GenerateSelfSigned(cfg.Server.RegistryDomain)
		if err != nil {
			return nil, err
		}
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
		slog.Info("generated self-signed certificate", "domain", cfg.Server.RegistryDomain)
		// Save to disk for reuse and Docker cert trust
		if err := tlsutil.SaveCertAndKey(cert, certFile, keyFile); err != nil {
			slog.Warn("failed to save cert", "error", err)
		} else {
			slog.Info("saved self-signed cert", "cert", certFile, "key", keyFile)
		}
	}

	srv := &http.Server{
		Addr:      ":5443",
		Handler:   r,
		TLSConfig: tlsConfig,
	}

	return srv, nil
}

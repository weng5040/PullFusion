package server

import (
	"crypto/tls"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/kspeeder/kspeeder-lite/internal/config"
	"github.com/kspeeder/kspeeder-lite/internal/downloader"
	"github.com/kspeeder/kspeeder-lite/internal/nodemgr"
	"github.com/kspeeder/kspeeder-lite/internal/registry"
	"github.com/kspeeder/kspeeder-lite/internal/tlsutil"
)

// Dependencies 服务器依赖
type Dependencies struct {
	Config     *config.Config
	NodeMgr    *nodemgr.Manager
	Downloader *downloader.MultiSourceDownloader
}

// NewRegistryServer 创建 registry HTTPS 服务器
func NewRegistryServer(cfg *config.Config, deps *Dependencies) (*http.Server, error) {
	r := chi.NewRouter()
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)
	r.Use(chimw.RealIP)

	// Registry API V2 路由 — 使用中间件拦截所有 /v2/ 路径
	regHandler := registry.NewHandler(cfg, deps.NodeMgr, deps.Downloader)

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
	var tlsConfig *tls.Config
	if cfg.Server.TLS.Cert != "" && cfg.Server.TLS.Key != "" {
		cert, err := tls.LoadX509KeyPair(cfg.Server.TLS.Cert, cfg.Server.TLS.Key)
		if err != nil {
			return nil, err
		}
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
	} else {
		// 自动生成自签证书
		cert, err := tlsutil.GenerateSelfSigned(cfg.Server.RegistryDomain)
		if err != nil {
			return nil, err
		}
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
		slog.Info("generated self-signed certificate", "domain", cfg.Server.RegistryDomain)
	}

	srv := &http.Server{
		Addr:      ":5443",
		Handler:   r,
		TLSConfig: tlsConfig,
	}

	return srv, nil
}

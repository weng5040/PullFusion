package registry

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/pullfusion/pullfusion/internal/auth"
)

// proxyManifest fetches a manifest through docker.1ms.run with token injection.
func (h *Handler) proxyManifest(w http.ResponseWriter, r *http.Request, name, ref, registry string) {
	reqStart := time.Now()
	slog.Info("manifest request", "name", name, "ref", ref, "registry", registry)

	// Accept header handling
	accept := r.Header.Get("Accept")
	if accept == "" {
		accept = "application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json"
	}
	manifestURL := fmt.Sprintf("https://docker.1ms.run/v2/%s/manifests/%s", name, ref)
	slog.Debug("manifest upstream", "url", manifestURL)

	req, err := http.NewRequestWithContext(r.Context(), r.Method, manifestURL, nil)
	if err != nil {
		slog.Error("manifest create request", "name", name, "error", err, "duration_ms", time.Since(reqStart).Milliseconds())
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Accept", accept)

	// Token injection
	tokenStart := time.Now()
	var tokenLen int
	if h.tokenSvc != nil {
		tok, tokErr := h.tokenSvc.GetToken(r.Context(), registry, name)
		if tokErr != nil {
			slog.Warn("manifest token fetch failed", "name", name, "registry", registry, "error", tokErr, "duration_ms", time.Since(tokenStart).Milliseconds())
		} else if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
			tokenLen = len(tok)
		}
	}
	slog.Info("manifest token ready", "name", name, "token_len", tokenLen, "duration_ms", time.Since(tokenStart).Milliseconds())

	// Execute upstream request
	upstreamStart := time.Now()
	resp, err := http.DefaultClient.Do(req)
	upstreamDuration := time.Since(upstreamStart)
	if err != nil {
		slog.Error("manifest upstream failed",
			"name", name,
			"ref", ref,
			"error", err,
			"duration_ms", upstreamDuration.Milliseconds(),
			"total_ms", time.Since(reqStart).Milliseconds(),
		)
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Log upstream response
	contentType := resp.Header.Get("Content-Type")
	contentDigest := resp.Header.Get("Docker-Content-Digest")
	slog.Info("manifest upstream response",
		"name", name,
		"ref", ref,
		"status", resp.StatusCode,
		"size", resp.ContentLength,
		"content_type", contentType,
		"digest", contentDigest,
		"upstream_ms", upstreamDuration.Milliseconds(),
	)

	// Handle redirects for HEAD requests
	if r.Method == http.MethodHead && (resp.StatusCode == http.StatusTemporaryRedirect || resp.StatusCode == http.StatusPermanentRedirect) {
		location := resp.Header.Get("Location")
		slog.Info("manifest redirect", "name", name, "status", resp.StatusCode, "location", location)
		if location != "" {
			redirectReq, err := http.NewRequestWithContext(r.Context(), "HEAD", location, nil)
			if err == nil {
				redirectReq.Header.Set("Accept", accept)
				resp.Body.Close()
				resp, err = http.DefaultClient.Do(redirectReq)
				if err != nil {
					slog.Warn("manifest redirect failed", "name", name, "location", location, "error", err)
					http.Error(w, "upstream redirect error", http.StatusBadGateway)
					return
				}
				defer resp.Body.Close()
			}
		}
	}

	// Transparent response
	transparentHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
	if r.Method != http.MethodHead {
		written, copyErr := io.Copy(w, resp.Body)
		totalDuration := time.Since(reqStart)
		if copyErr != nil {
			slog.Error("manifest write failed", "name", name, "written", written, "error", copyErr)
		} else {
			slog.Info("manifest complete",
				"name", name,
				"ref", ref,
				"status", resp.StatusCode,
				"size", written,
				"total_ms", totalDuration.Milliseconds(),
			)
		}
	}
}

// proxyManifest is an alias for Handler method use
var _ = (*Handler).proxyManifest

// auth package used by resolver
var _ = (*auth.TokenService)(nil)

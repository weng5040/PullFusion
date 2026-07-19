package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
)

// MirrorAuth maps mirror hostname to its auth config.
type MirrorAuth struct {
	Realm   string
	Service string
}

// KnownMirrorAuths lists mirrors with known auth endpoints and their services.
var KnownMirrorAuths = map[string]MirrorAuth{
	"docker.1ms.run":           {Realm: "https://docker.1ms.run/openapi/v1/auth/token", Service: "docker.1ms.run"},
	"docker.xuanyuan.me":       {Realm: "https://docker.xuanyuan.me/token/auth.docker.io", Service: "registry.docker.io"},
	"docker.m.daocloud.io":     {Realm: "https://m.daocloud.io/auth/token", Service: "docker.m.daocloud.io"},
	"docker.sparkcr.cn":        {Realm: "https://docker.sparkcr.cn/token", Service: "sparkcr"},
	"docker.367231.xyz":        {Realm: "https://docker.367231.xyz/token", Service: "registry.docker.io"},
	"docker-registry.nmqu.com": {Realm: "https://docker-registry.nmqu.com/token", Service: "registry.docker.io"},
}

// NoAuthMirrors are mirrors that serve content without requiring any authentication token.
var NoAuthMirrors = map[string]bool{
	"docker.1panel.live":  true,
	"hub.1panel.dev":      true,
	"dockerproxy.cool":    true,
	"hub1.nat.tf":         true,
	"hub4.nat.tf":         true,
	"dockerproxy.net":     true,
}

// TokenService manages registry tokens with caching.
type TokenService struct {
	mu         sync.RWMutex
	tokenCache map[string]string
}

// NewTokenService creates a new TokenService.
func NewTokenService() *TokenService {
	return &TokenService{
		tokenCache: make(map[string]string),
	}
}

// GetToken returns a token for the given registry and image (legacy: docker.1ms.run).
func (s *TokenService) GetToken(ctx context.Context, registry, imageName string) (string, error) {
	if registry == "dockerhub" {
		return s.GetMirrorToken(ctx, "https://docker.1ms.run", imageName)
	}
	return "", fmt.Errorf("no token source for registry %s", registry)
}

// GetMirrorToken fetches a token for a specific mirror URL.
func (s *TokenService) GetMirrorToken(ctx context.Context, mirrorURL, imageName string) (string, error) {
	host := strings.TrimPrefix(mirrorURL, "https://")
	host = strings.TrimPrefix(host, "http://")
	if idx := strings.Index(host, "/"); idx >= 0 {
		host = host[:idx]
	}
	host = strings.TrimSuffix(host, ":443")

	// No auth needed for these mirrors
	if NoAuthMirrors[host] {
		return "", nil
	}

	auth, ok := KnownMirrorAuths[host]
	if !ok {
		return "", fmt.Errorf("no auth config for mirror %s", host)
	}

	cacheKey := host + ":" + imageName
	s.mu.RLock()
	if token, ok := s.tokenCache[cacheKey]; ok {
		s.mu.RUnlock()
		return token, nil
	}
	s.mu.RUnlock()

	authURL := fmt.Sprintf("%s?service=%s&scope=repository:%s:pull",
		auth.Realm, auth.Service, imageName)
	slog.Info("fetching mirror token", "mirror", host, "name", imageName)

	req, err := http.NewRequestWithContext(ctx, "GET", authURL, nil)
	if err != nil {
		return "", fmt.Errorf("create token request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request failed for %s: %w", host, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token request for %s: HTTP %d", host, resp.StatusCode)
	}

	var tokenResp struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("decode token from %s: %w", host, err)
	}

	token := tokenResp.Token
	if token == "" {
		token = tokenResp.AccessToken
	}
	if token == "" {
		return "", fmt.Errorf("empty token in response from %s", host)
	}

	s.mu.Lock()
	s.tokenCache[cacheKey] = token
	s.mu.Unlock()

	slog.Info("mirror token acquired", "mirror", host, "len", len(token))
	return token, nil
}

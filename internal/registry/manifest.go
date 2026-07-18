package registry

import (
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/pullfusion/pullfusion/internal/nodemgr"
	"github.com/pullfusion/pullfusion/internal/config"
)

// proxyManifest 单节点 manifest 代理。
// 选1个健康节点 → 转发 GET/HEAD → 透传响应 headers 和 body。
// registry 参数用于选择节点来源（dockerhub/ghcr）。
func (h *Handler) proxyManifest(w http.ResponseWriter, r *http.Request, name, reference, registry string) {
		// Manifest: prefer fast nodes (Speed>0, from config) over fetched nodes (Speed=0, priority>=50)
	allNodes := h.nodeMgr.List()
	var fastNodes []*nodemgr.Node
	for _, n := range allNodes {
		if n.Enabled && n.Healthy && n.Speed > 0 {
			for _, t := range n.Targets {
				if t == registry {
					fastNodes = append(fastNodes, n)
					break
				}
			}
		}
	}
	var nodes []*nodemgr.Node
	if len(fastNodes) > 0 {
		nodes = fastNodes[:min(len(fastNodes), 1)]
	} else {
		nodes = h.nodeMgr.SelectForBlob(registry, 0, 1)
	}
	if len(nodes) == 0 {
		http.Error(w, "no healthy nodes available", http.StatusBadGateway)
		return
	}

	node := nodes[0]
	manifestURL := strings.TrimRight(node.URL, "/") + "/v2/" + name + "/manifests/" + reference

	req, err := http.NewRequestWithContext(r.Context(), r.Method, manifestURL, nil)
	if err != nil {
		slog.Error("create manifest request", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// 获取 dockerhub token
	if registry == "dockerhub" && h.tokenSvc != nil {
		if tok, err := h.tokenSvc.GetToken(r.Context(), registry, name); err == nil && tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}

	// 获取 dockerhub token
	if registry == "dockerhub" && h.tokenSvc != nil {
		if tok, err := h.tokenSvc.GetToken(r.Context(), registry, name); err == nil && tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}

	// 获取 dockerhub token
	if registry == "dockerhub" && h.tokenSvc != nil {
		if tok, err := h.tokenSvc.GetToken(r.Context(), registry, name); err == nil && tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}

	// 获取 dockerhub token
	if registry == "dockerhub" && h.tokenSvc != nil {
		if tok, err := h.tokenSvc.GetToken(r.Context(), registry, name); err == nil && tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}

	// 透传关键 headers
	copyHeader(req.Header, r.Header, "Accept")
	copyHeader(req.Header, r.Header, "Authorization")

	// 如果节点配置了 token，添加进去
	if token := getNodeToken(h.cfg, node.URL); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Error("manifest fetch failed", "node", node.URL, "error", err)
		h.nodeMgr.MarkFailed(node)
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotModified {
		h.nodeMgr.MarkSuccess(node)
	}

	// 透传响应 headers 和 body
	transparentHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// getNodeToken 获取节点的 token（支持 dockerhub 和 ghcr）
func getNodeToken(cfg *config.Config, nodeURL string) string {
	// ghcr
	for _, m := range cfg.Mirrors.Ghcr {
		if m.URL == nodeURL && m.Token != "" {
			return m.Token
		}
	}
	// dockerhub
	for _, m := range cfg.Mirrors.Dockerhub {
		if m.URL == nodeURL && m.Token != "" {
			return m.Token
		}
	}
	return ""
}

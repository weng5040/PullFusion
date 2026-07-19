package registry

import (
	"io"
	"log/slog"
	"net/http"
)

// proxyManifest proxies manifest requests through docker.1ms.run (always reachable).
func (h *Handler) proxyManifest(w http.ResponseWriter, r *http.Request, name, reference, registry string) {
	manifestURL := "https://docker.1ms.run/v2/" + name + "/manifests/" + reference

	req, err := http.NewRequestWithContext(r.Context(), r.Method, manifestURL, nil)
	if err != nil {
		slog.Error("create manifest request", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	copyHeader(req.Header, r.Header, "Accept")
	copyHeader(req.Header, r.Header, "Authorization")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Error("manifest fetch failed", "error", err)
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	transparentHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

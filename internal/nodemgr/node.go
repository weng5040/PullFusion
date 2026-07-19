package nodemgr

import "sync"

// NodeType defines the type of a node.
type NodeType string

const (
	NodeTypeMirror NodeType = "mirror"
	NodeTypeSocks5 NodeType = "socks5"
	NodeTypeHTTP   NodeType = "http"
)

// Node represents a registry mirror or proxy node.
type Node struct {
	URL         string   `json:"url"`
	DisplayName string   `json:"display_name"`
	Type        NodeType `json:"type"`
	Priority    int      `json:"priority"`
	Enabled     bool     `json:"enabled"`
	Healthy     bool     `json:"healthy"`
	Targets     []string `json:"targets"`       // e.g. ["dockerhub", "ghcr"]
	Token       string   `json:"token,omitempty"` // config-level auth token

	mu sync.Mutex
}

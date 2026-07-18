package downloader

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"

	"github.com/pullfusion/pullfusion/internal/nodemgr"
)

// Range 下载区间
type Range struct {
	Start int64
	End   int64 // exclusive, -1 表示到末尾
}

// DownloadRequest 下载请求
type DownloadRequest struct {
	Name         string
	Digest       string
	ExpectedSize int64
	Range        *Range
	Registry     string
}

// Chunk 分块——由 chunker 分配，承载下载区间、指定节点和下载结果
type Chunk struct {
	Index  int
	Start  int64
	End    int64 // exclusive
	Node   *nodemgr.Node
	Reader io.ReadCloser
	Err    error
}

// MultiSourceDownloader 多源并发下载器
type MultiSourceDownloader struct {
	nodeMgr       *nodemgr.Manager
	maxConcurrent int
	globalSem     chan struct{}
	chunkMinSize  int64
	chunkMaxSize  int64
	failThreshold int
	chunker       *Chunker
}

// NewMultiSourceDownloader 创建下载器
func NewMultiSourceDownloader(nodeMgr *nodemgr.Manager, maxConcurrent, maxGlobal int, chunkMin, chunkMax int64, failThreshold int) *MultiSourceDownloader {
	return &MultiSourceDownloader{
		nodeMgr:       nodeMgr,
		maxConcurrent: maxConcurrent,
		globalSem:     make(chan struct{}, maxGlobal),
		chunkMinSize:  chunkMin,
		chunkMaxSize:  chunkMax,
		failThreshold: failThreshold,
		chunker:       NewChunker(chunkMin, chunkMax),
	}
}

// Download 多源下载 blob，返回 (reader, contentLength, nodeCount, error)
func (d *MultiSourceDownloader) Download(ctx context.Context, req DownloadRequest) (io.ReadCloser, int64, int, error) {
	nodes := d.nodeMgr.SelectForBlob(req.Registry, req.ExpectedSize, d.maxConcurrent)
	if len(nodes) == 0 {
		return nil, 0, 0, fmt.Errorf("no nodes available")
	}

	healthyNodes, totalSize, err := d.headProbe(ctx, nodes, req)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("head probe: %w", err)
	}
	if len(healthyNodes) == 0 {
		return nil, 0, 0, fmt.Errorf("no healthy nodes after HEAD probe")
	}

	if totalSize <= 0 {
		totalSize = req.ExpectedSize
	}

	nodeCount := len(healthyNodes)

	if nodeCount == 1 {
		r, sz, err := d.singleNodeDownload(ctx, req, healthyNodes[0])
		return r, sz, nodeCount, err
	}

	r, sz, err := d.multiNodeDownload(ctx, req, healthyNodes, totalSize)
	return r, sz, nodeCount, err
}

// headProbe 并发 HEAD 探测所有节点
func (d *MultiSourceDownloader) headProbe(ctx context.Context, nodes []*nodemgr.Node, req DownloadRequest) ([]*nodemgr.Node, int64, error) {
	type probeResult struct {
		node *nodemgr.Node
		size int64
		ok   bool
	}

	var wg sync.WaitGroup
	results := make(chan probeResult, len(nodes))
	sem := make(chan struct{}, d.maxConcurrent)

	for _, n := range nodes {
		wg.Add(1)
		node := n
		go func() {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			blobURL := fmt.Sprintf("%s/v2/%s/blobs/%s", node.URL, req.Name, req.Digest)

			httpReq, err := http.NewRequestWithContext(ctx, "HEAD", blobURL, nil)
			if err != nil {
				results <- probeResult{node: node, ok: false}
				return
			}

			// 如果节点有 token，添加认证头
			if node.Token != "" {
				httpReq.Header.Set("Authorization", "Bearer "+node.Token)
			}

			resp, err := http.DefaultClient.Do(httpReq)
			if err != nil {
				d.nodeMgr.MarkFailed(node)
				results <- probeResult{node: node, ok: false}
				return
			}
			resp.Body.Close()
			slog.Info("head probe", "node", node.DisplayName, "status", resp.StatusCode, "url", blobURL)

			if resp.StatusCode != http.StatusOK {
				d.nodeMgr.MarkFailed(node)
				results <- probeResult{node: node, ok: false}
				return
			}

			d.nodeMgr.MarkSuccess(node)
			results <- probeResult{node: node, size: resp.ContentLength, ok: true}
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var healthy []*nodemgr.Node
	var confirmedSize int64
	for r := range results {
		if r.ok {
			healthy = append(healthy, r.node)
			if r.size > confirmedSize {
				confirmedSize = r.size
			}
		}
	}

	return healthy, confirmedSize, nil
}

// multiNodeDownload 多节点并发分块下载 + 流式合并
func (d *MultiSourceDownloader) multiNodeDownload(
	ctx context.Context,
	req DownloadRequest,
	nodes []*nodemgr.Node,
	totalSize int64,
) (io.ReadCloser, int64, error) {
	chunks := d.chunker.Allocate(nodes, totalSize, req.Range)
	if len(chunks) == 0 {
		slog.Warn("chunk allocation empty, falling back to single node", "blob", req.Digest, "nodes", len(nodes))
		return d.singleNodeDownload(ctx, req, nodes[0])
	}

	slog.Info("multi-source download",
		"blob", req.Digest,
		"nodes", len(nodes),
		"chunks", len(chunks),
		"size", totalSize,
	)

	pr, pw := io.Pipe()
	results := make(chan *Chunk, len(chunks))

	var wg sync.WaitGroup
	sem := make(chan struct{}, d.maxConcurrent)

	for _, ch := range chunks {
		wg.Add(1)
		chunk := ch

		select {
		case d.globalSem <- struct{}{}:
		case <-ctx.Done():
			wg.Done()
			continue
		}

		sem <- struct{}{}

		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() { <-d.globalSem }()

			d.nodeMgr.IncrInflight(chunk.Node)
			defer d.nodeMgr.DecrInflight(chunk.Node)

			if err := downloadChunkData(ctx, chunk, req, chunk.Node); err != nil {
				d.nodeMgr.MarkFailed(chunk.Node)
				chunk.Err = err
			} else {
				d.nodeMgr.MarkSuccess(chunk.Node)
			}

			results <- chunk
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	go func() {
		if err := mergeChunksStream(ctx, len(chunks), results, pw, d.nodeMgr, req, d.failThreshold); err != nil {
			pw.CloseWithError(err)
			return
		}
		pw.Close()
	}()

	return pr, totalSize, nil
}

// singleNodeDownload 单节点下载
func (d *MultiSourceDownloader) singleNodeDownload(ctx context.Context, req DownloadRequest, node *nodemgr.Node) (io.ReadCloser, int64, error) {
	blobURL := fmt.Sprintf("%s/v2/%s/blobs/%s", node.URL, req.Name, req.Digest)

	httpReq, err := http.NewRequestWithContext(ctx, "GET", blobURL, nil)
	if err != nil {
		return nil, 0, err
	}

	if req.Range != nil {
		if req.Range.End > 0 {
			httpReq.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", req.Range.Start, req.Range.End-1))
		} else {
			httpReq.Header.Set("Range", fmt.Sprintf("bytes=%d-", req.Range.Start))
		}
	}

	// 如果节点有 token，添加认证头
	if node.Token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+node.Token)
	}

	d.nodeMgr.IncrInflight(node)
	defer d.nodeMgr.DecrInflight(node)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		d.nodeMgr.MarkFailed(node)
		return nil, 0, err
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
			slog.Info("head probe", "node", node.DisplayName, "status", resp.StatusCode, "url", blobURL)
		d.nodeMgr.MarkFailed(node)
		return nil, 0, fmt.Errorf("upstream returned %d", resp.StatusCode)
	}

	d.nodeMgr.MarkSuccess(node)
	return resp.Body, resp.ContentLength, nil
}

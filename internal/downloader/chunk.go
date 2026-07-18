package downloader

import (
	"log/slog"
	"github.com/pullfusion/pullfusion/internal/nodemgr"
)

// Chunker 分块管理器——按节点权重将下载范围拆分为多个分块
type Chunker struct {
	MinSize int64
	MaxSize int64
}

// NewChunker 创建分块器
func NewChunker(minSize, maxSize int64) *Chunker {
	return &Chunker{MinSize: minSize, MaxSize: maxSize}
}

// Allocate 按节点权重将 totalSize 字节拆分为分块，每个分块分配给对应节点。
//
// 权重计算：nodeWeight = speed / priority（speed 或 priority ≤ 0 时取 1）
// 分块大小: clamp(rangeLen * nodeWeight / sumWeights, [MinSize, MaxSize])
// 最后一个分块自动拉伸到 rangeEnd 以覆盖全部数据。
func (c *Chunker) Allocate(nodes []*nodemgr.Node, totalSize int64, r *Range) []*Chunk {
	if len(nodes) == 0 {
		return nil
	}

	rangeStart := int64(0)
	rangeEnd := totalSize
	if r != nil {
		rangeStart = r.Start
		rangeEnd = r.End
	}
	rangeLen := rangeEnd - rangeStart
	if rangeLen <= 0 {
		return nil
	}

	// 计算每个节点的权重
	weights := make([]float64, len(nodes))
	sumW := 0.0
	for i, n := range nodes {
		w := nodeWeight(n)
		weights[i] = w
		sumW += w
	}
	if sumW == 0 {
		sumW = float64(len(nodes))
		for i := range weights {
			weights[i] = 1.0
		}
	}

	// 按权重分配分块
	var chunks []*Chunk
	offset := rangeStart

	for i, n := range nodes {
		chunkSize := int64(float64(rangeLen) * weights[i] / sumW)
		chunkSize = clamp(chunkSize, c.MinSize, c.MaxSize)

		// 不超出剩余范围
		if offset+chunkSize > rangeEnd {
			chunkSize = rangeEnd - offset
		}
		if chunkSize <= 0 {
			continue
		}

		chunks = append(chunks, &Chunk{
			Index: len(chunks),
			Start: offset,
			End:   offset + chunkSize,
			Node:  n,
		})
		offset += chunkSize
	}

	// 最后一个分块覆盖剩余范围（处理 clamp 造成的尾部空缺）
	if len(chunks) > 0 && offset < rangeEnd {
		chunks[len(chunks)-1].End = rangeEnd
	}

	// Fallback: if somehow no chunks were allocated, use first node for entire range
	if len(chunks) == 0 && len(nodes) > 0 {
		slog.Warn("chunk allocation fallback, using single chunk", "size", rangeLen)
		chunks = append(chunks, &Chunk{
			Index: 0,
			Start: rangeStart,
			End:   rangeEnd,
			Node:  nodes[0],
		})
	}
	return chunks
}

// nodeWeight 根据优先级和速度计算节点权重
func nodeWeight(n *nodemgr.Node) float64 {
	p := float64(n.Priority)
	if p <= 0 {
		p = 1
	}
	speed := n.Speed
	if speed <= 0 {
		speed = 1
	}
	return speed / p
}

func clamp(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

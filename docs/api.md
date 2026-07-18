# PullFusion 管理 API 文档

## 基础信息

- **基础 URL：** `http://<host>:5003`
- **Content-Type：** `application/json`
- **认证：** 支持 Basic Auth（通过 `server.proxy_auth` 配置）

## API 概览

| 方法 | 端点 | 说明 |
|------|------|------|
| `GET` | `/healthz` | 健康检查 |
| `GET` | `/admin/nodes` | 节点列表 + 状态 |
| `POST` | `/admin/nodes/{id}/test` | 触发单个节点测速 |
| `GET` | `/admin/stats` | 全局下载统计 |
| `POST` | `/admin/config/reload` | 手动重载配置 |
| `GET` | `/metrics` | Prometheus 指标 |
| `GET` | `/dashboard` | Web 管理仪表盘 |

---

## GET /healthz — 健康检查

返回服务运行状态和节点健康概览，常用于 K8s liveness/readiness probe。

### curl 示例

```bash
curl http://localhost:5003/healthz
```

### 响应示例 (200 OK)

```json
{
  "status": "ok",
  "uptime": "2h34m12s",
  "nodes_total": 9,
  "nodes_healthy": 8,
  "active_downloads": 0
}
```

### 字段说明

| 字段 | 类型 | 说明 |
|------|------|------|
| `status` | string | 总是 `"ok"` |
| `uptime` | string | 服务运行时间 |
| `nodes_total` | int | 节点总数（含已熔断） |
| `nodes_healthy` | int | 当前健康节点数 |
| `active_downloads` | int | 当前活跃下载数 |

---

## GET /admin/nodes — 节点列表

返回所有节点的完整状态信息。

### curl 示例

```bash
curl http://localhost:5003/admin/nodes
```

### 响应示例 (200 OK)

```json
[
  {
    "url": "https://docker.1ms.run",
    "display_name": "1ms",
    "type": "mirror",
    "priority": 1,
    "enabled": true,
    "speed_mbps": 45.2,
    "healthy": true,
    "fail_count": 0,
    "in_flight": 2,
    "last_check": "2026-07-19T10:30:00Z"
  },
  {
    "url": "socks5://192.168.1.10:1080",
    "display_name": "proxy-ss-1",
    "type": "socks5",
    "priority": 1,
    "enabled": true,
    "speed_mbps": 12.8,
    "healthy": true,
    "fail_count": 1,
    "in_flight": 1,
    "last_check": "2026-07-19T10:30:05Z"
  }
]
```

### 字段说明

| 字段 | 类型 | 说明 |
|------|------|------|
| `url` | string | 节点完整 URL |
| `display_name` | string | 显示名称 |
| `type` | string | `mirror` / `socks5` / `http` |
| `priority` | int | 优先级（越小越优先） |
| `enabled` | bool | 是否启用 |
| `speed_mbps` | float64 | 最近测速结果 (Mbps) |
| `healthy` | bool | 健康状态，false 表示已熔断 |
| `fail_count` | int | 连续失败次数 |
| `in_flight` | int32 | 当前并发下载数 |
| `last_check` | string | 最近一次测速/检查时间 (RFC 3339) |

---

## POST /admin/nodes/{id}/test — 触发测速

对指定节点立即执行测速（异步）。节点通过 URL 或 display_name 查找。

### curl 示例

```bash
# 通过 display_name 触发
curl -X POST http://localhost:5003/admin/nodes/1ms/test

# 通过完整 URL 触发
curl -X POST http://localhost:5003/admin/nodes/https%3A%2F%2Fdocker.1ms.run/test
```

### 响应示例 (200 OK)

```json
{
  "status": "testing",
  "node": "https://docker.1ms.run"
}
```

### 错误响应

```json
// 节点不存在 (404)
{
  "error": "node not found"
}

// 缺少节点 ID (400)
{
  "error": "missing node id"
}
```

**注意：** 测速结果通过 GET /admin/nodes 的 `speed_mbps` 字段异步更新。

---

## GET /admin/stats — 全局统计

返回全局下载统计，包括成功/失败/错误率。

### curl 示例

```bash
curl http://localhost:5003/admin/stats
```

### 响应示例 (200 OK)

```json
{
  "nodes_total": 9,
  "nodes_healthy": 8,
  "active_downloads": 3,
  "completed": 156,
  "failed": 4,
  "error_rate": 0.025
}
```

### 字段说明

| 字段 | 类型 | 说明 |
|------|------|------|
| `nodes_total` | int | 总节点数 |
| `nodes_healthy` | int | 健康节点数 |
| `active_downloads` | int64 | 当前活跃下载数 |
| `completed` | int64 | 累计成功下载数 |
| `failed` | int64 | 累计失败下载数 |
| `error_rate` | float64 | 失败率 = failed / (completed + failed) |

---

## POST /admin/config/reload — 配置重载

手动触发配置文件重载，使 nodes.yaml 修改生效。

### curl 示例

```bash
curl -X POST http://localhost:5003/admin/config/reload
```

### 响应示例 (200 OK)

```json
{
  "status": "config reloaded"
}
```

### 错误响应

```json
// 热加载不可用 (501)
{
  "status": "reload not available",
  "error": "no reload function configured"
}

// 配置解析失败 (500)
{
  "status": "failed",
  "error": "yaml: unmarshal errors: line 10: cannot unmarshal..."
}
```

---

## GET /metrics — Prometheus 指标

返回 Prometheus 格式的指标数据，可通过 Prometheus + Grafana 进行监控。

### curl 示例

```bash
curl http://localhost:5003/metrics
```

### 指标说明

| 指标名称 | 类型 | 标签 | 说明 |
|---------|------|------|------|
| `pullfusion_blob_downloads_total` | Counter | `registry`, `status` | blob 下载总数，status=success/error |
| `pullfusion_blob_download_duration_seconds` | Histogram | `registry` | blob 下载耗时分布 (0.1s ~ 819.2s) |
| `pullfusion_blob_download_bytes` | Counter | — | 已下载总字节数 |
| `pullfusion_node_speed_mbps` | Gauge | `node` | 各节点当前测速结果 (Mbps) |
| `pullfusion_node_health` | Gauge | `node` | 节点健康状态 (1=健康, 0=熔断) |
| `pullfusion_node_inflight` | Gauge | `node` | 节点当前并发下载数 |
| `pullfusion_active_downloads` | Gauge | — | 当前全局活跃下载数 |
| `pullfusion_config_reloads_total` | Counter | — | 配置重载总次数 |

### 常用 PromQL 查询

```promql
# 下载成功率
sum(pullfusion_blob_downloads_total{status="success"}) / sum(pullfusion_blob_downloads_total)

# 下载速率 (bytes/s)
rate(pullfusion_blob_download_bytes[5m])

# 不健康节点数
count(pullfusion_node_health == 0)

# P99 下载耗时
histogram_quantile(0.99, rate(pullfusion_blob_download_duration_seconds_bucket[5m]))
```

---

## GET /dashboard — Web 仪表盘

Web 管理界面，提供可视化的节点状态和下载统计。

### 访问方式

```
浏览器打开：http://<host>:5003/dashboard
```

仪表盘提供：
- 节点状态面板（名称、速度、健康状态、并发数）
- 下载统计（成功/失败/错误率）
- 节点测速触发按钮
- 配置重载按钮

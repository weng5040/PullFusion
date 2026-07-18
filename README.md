# <img src="https://raw.githubusercontent.com/weng5040/kspeeder-lite/main/assets/logo.png" width="32"> PullFusion

> **Multi-source Bandwidth Fusion for Docker Images** — 多源带宽叠加，让镜像拉取快如闪电。

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat&logo=go" alt="Go">
  <img src="https://img.shields.io/badge/License-MIT-blue.svg" alt="License">
  <img src="https://img.shields.io/badge/Version-1.0.0-green.svg" alt="Version">
  <img src="https://img.shields.io/badge/Docker-Supported-2496ED?style=flat&logo=docker" alt="Docker">
</p>

---

PullFusion 是一个开源的多源带宽叠加 Docker 镜像代理，通过 **多 mirror + socks5/http 代理并发分块下载** 实现 2~3 倍带宽叠加；提供 **registry-mirror 与 CONNECT 代理双模式** 集成；**开箱即用** — 内置 5 个公共 mirror，一条 docker compose 命令即可部署。

---

## 为什么选择 PullFusion？

|  | PullFusion | 单一 Mirror | 其他方案 |
|--|-----------|------------|---------|
| **带宽叠加** | 多源并发，2~3x | 单源，无叠加 | 通常无叠加 |
| **集成方式** | registry-mirror + CONNECT 代理 | 仅 registry-mirror | 单一模式 |
| **故障转移** | 自动熔断 + 多级 fallback | 单点故障 | 有限 or 手动 |
| **代理节点** | socks5/http 代理节点融合 | 不支持 | 通常不支持 |
| **管理面板** | 内置 Web Dashboard | 无 | 无 or 外部 |
| **配置热加载** | 自动检测 + 手动 API | 需重启 | 需重启 |

---

## 快速开始

### Step 1 — 克隆仓库

```bash
git clone https://github.com/weng5040/kspeeder-lite.git pullfusion
cd pullfusion
```

### Step 2 — 准备配置并启动

```bash
mkdir -p docker/config docker/cache
cp configs/nodes.sample.yaml docker/config/nodes.yaml
cd docker && docker compose up -d
```

### Step 3 — 拉取镜像验证

```bash
docker pull nginx:latest
```

---

## 核心特性

### 多源带宽叠加

多个 mirror 与 socks5/http 代理节点并发分块下载同一 blob，将多路带宽线性叠加。实测 1 个 mirror + 2 个 socks5 代理可稳定达到 2~3x 加速比。

### 双模式集成

| 模式 | 端口 | 适用场景 |
|------|------|---------|
| **registry-mirror** | `5443` (HTTPS) | 内网部署，仅加速 dockerhub |
| **CONNECT 代理** | `5003` (HTTP) | 多 registry 支持，免配置证书 |

### 智能负载均衡

4 维加权评分算法自动选择最优节点：
- **Speed** — 最近测速带宽 (Mbps)
- **Priority** — 用户配置的优先级
- **Health** — 健康状态（熔断/正常）
- **Load** — 当前并发数

### 熔断恢复

连续失败 3 次自动熔断节点，后台探测每 30s 探活，测速通过后自动恢复。

### 本地缓存

LRU 策略的 blob 磁盘缓存，命中后直接响应略过上流下载，二次拉取速度提升 10x+。

### 可观测性

- **Web Dashboard** — `/dashboard` 嵌入式管理面板
- **Prometheus** — `/metrics` 导出 8 个核心指标
- **健康检查** — `/healthz` 端点，支持 K8s probe

---

## 架构概览

```
                        docker pull nginx:latest
                              │
              ┌───────────────┴───────────────┐
              ▼                               ▼
     registry-mirror 模式              CONNECT 代理模式
     dockerd → :5443 (HTTPS)          dockerd → :5003 (HTTP Tunnel)
              │                               │
              └───────────────┬───────────────┘
                              ▼
              ┌───────────────────────────────┐
              │         PullFusion Core       │
              │                               │
              │  ┌─────────────────────────┐  │
              │  │     Load Balancer        │  │
              │  │   Speed × Priority ×     │  │
              │  │   Health × Load          │  │
              │  └───────────┬─────────────┘  │
              │              │                │
              │   ┌──────────┼──────────┐     │
              │   ▼          ▼          ▼     │
              │ ┌──────┐ ┌──────┐ ┌──────┐   │
              │ │Mirror│ │Mirror│ │Socks5│   │
              │ │  #1  │ │  #2  │ │Proxy │   │
              │ └──┬───┘ └──┬───┘ └──┬───┘   │
              │    │         │        │       │
              │    │    io.Pipe Streaming     │
              │    └─────────┼────────┘       │
              │              ▼                │
              │        ┌──────────┐           │
              │        │  Client  │           │
              │        └──────────┘           │
              └───────────────────────────────┘
```

---

## Dockerd 集成

### 方式 A：registry-mirror 模式

```json
// /etc/docker/daemon.json
{
  "registry-mirrors": ["https://<host>:5443"],
  "insecure-registries": ["<host>:5443"]
}
```

```bash
sudo systemctl restart docker
```

### 方式 B：CONNECT 代理模式（推荐）

```json
// /etc/docker/daemon.json
{
  "registry-mirrors": ["https://registry.local"]
}
```

```ini
# /etc/systemd/system/docker.service.d/proxy.conf
[Service]
Environment="HTTP_PROXY=http://<host>:5003"
Environment="HTTPS_PROXY=http://<host>:5003"
Environment="NO_PROXY=127.0.0.1,localhost"
```

```bash
sudo systemctl daemon-reload
sudo systemctl restart docker
```

---

## 管理面板

浏览器访问 `http://<host>:5003/dashboard` 打开嵌入式 Web 管理面板：

- **节点状态面板** — 实时显示各节点的名称、类型、速度、健康状态和并发数
- **下载统计** — 成功/失败计数、错误率、活跃下载数
- **节点操作** — 一键触发单节点测速
- **配置管理** — 手动触发配置热加载

---

## API 端点

| 方法 | 端点 | 说明 |
|------|------|------|
| `GET` | `/healthz` | 健康检查 |
| `GET` | `/admin/nodes` | 节点列表及状态 |
| `POST` | `/admin/nodes/{id}/test` | 触发单节点测速 |
| `GET` | `/admin/stats` | 全局下载统计 |
| `POST` | `/admin/config/reload` | 手动配置重载 |
| `GET` | `/metrics` | Prometheus 指标 |
| `GET` | `/dashboard` | Web 管理仪表盘 |

---

## 平台支持

| 平台 | amd64 | arm64 | armv7 |
|------|:-----:|:-----:|:-----:|
| Linux (Docker) |  ✔️ |  ✔️ |  ✔️ |
| 群晖 DSM 7.x+ |  ✔️ | — | — |
| OpenWrt / 软路由 |  ✔️ |  ✔️ |  ✔️ |
| Raspberry Pi | — |  ✔️ |  ✔️ |

---

## 项目结构

```
pullfusion/
├── cmd/pullfusion/         # 应用入口
├── internal/
│   ├── admin/              # 管理 API + Web Dashboard
│   ├── auth/               # DockerHub / GHCR token 鉴权
│   ├── config/             # 配置加载与热更新 (fsnotify)
│   ├── downloader/         # 多源并发分块下载器
│   ├── metrics/            # Prometheus 指标
│   ├── nodemgr/            # 节点管理与负载均衡
│   ├── proxyclient/        # socks5/http 代理拨号
│   ├── registry/           # Docker Registry API V2 实现
│   ├── server/             # Registry HTTPS + CONNECT 代理
│   └── tlsutil/            # 自签证书生成
├── pkg/
│   └── version/            # 版本信息
├── configs/                # 配置模板
├── docker/                 # Dockerfile + Compose
├── docs/                   # 文档
├── scripts/                # 构建与测试脚本
└── test/                   # 测试用例
```

---

## 开发

```bash
# 本地运行
go run ./cmd/pullfusion -config configs/nodes.sample.yaml

# 构建
bash scripts/build.sh

# 测试
bash scripts/test.sh

# 代码检查
golangci-lint run
```

---

## 文档

- [API 文档](docs/api.md)
- [部署指南](docs/deployment.md)
- [故障排查](docs/troubleshooting.md)

---

## 致谢

PullFusion 基于 [kspeeder-lite](https://github.com/weng5040/kspeeder-lite) 演变而来，感谢所有贡献者。

## License

[MIT](LICENSE)

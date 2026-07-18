# PullFusion 部署指南

## 环境要求

| 组件 | 最低版本 | 说明 |
|------|---------|------|
| Docker | 20.10+ | 需支持 `docker compose` 插件 |
| Docker Compose | v2.0+ | Compose V2 插件或独立安装 |
| Go（源码构建）| 1.22+ | 仅源码构建时需要 |
| 操作系统 | Linux (amd64/arm64/armv7) | 支持 Docker 的 Linux 发行版 |

**dockerd 集成要求：**
- registry-mirror 模式：dockerd 版本无特殊要求
- CONNECT 代理模式：dockerd ≥ 17.07（支持 `HTTPS_PROXY`）

## 开发服务器快速部署

```bash
# 1. 克隆仓库
git clone https://github.com/PullFusion/PullFusion.git
cd PullFusion

# 2. 准备配置目录和缓存目录
mkdir -p docker/config docker/cache

# 3. 复制示例配置并编辑
cp configs/nodes.sample.yaml docker/config/nodes.yaml
vim docker/config/nodes.yaml  # 可选：添加 socks5 代理节点

# 4. 启动服务
cd docker && docker compose up -d

# 5. 查看日志
docker compose logs -f
```

**首次启动后：**
- Registry 端口：`5443`（HTTPS）
- 代理端口：`5003`（HTTP，含管理 API）
- 缓存目录：`docker/cache/`（blob 本地缓存）

## dockerd 集成配置

### 方式 A：registry-mirror 模式

在 dockerd 配置中添加 PullFusion 作为 mirror：

```json
// /etc/docker/daemon.json
{
  "registry-mirrors": ["https://<PullFusion-ip>:5443"],
  "insecure-registries": ["<PullFusion-ip>:5443"]
}
```

适用场景：
- PullFusion 在同一台机器或内网
- 只需 dockerhub 加速
- 简单直接，无需修改代理设置

### 方式 B：CONNECT 代理模式（推荐）

通过 HTTPS_PROXY 将 dockerd 的 registry 流量指向 PullFusion 的 CONNECT 隧道：

```json
// /etc/docker/daemon.json
{
  "registry-mirrors": ["https://registry.local"]
}
```

```ini
# /etc/systemd/system/docker.service.d/proxy.conf
[Service]
Environment="HTTP_PROXY=http://<PullFusion-ip>:5003"
Environment="HTTPS_PROXY=http://<PullFusion-ip>:5003"
Environment="NO_PROXY=127.0.0.1,localhost"
```

```bash
# 重载并重启 dockerd
sudo systemctl daemon-reload
sudo systemctl restart docker
```

优势：
- 无需配置 insecure-registries
- 支持多 registry（dockerhub + ghcr）
- 自动处理 TLS 证书
- 支持 Basic Auth 鉴权（`server.proxy_auth`）

### 验证集成

```bash
# 测试拉取镜像
docker pull nginx:latest
docker pull alpine:latest

# 查看 PullFusion 日志确认流量经过代理
docker compose logs PullFusion | grep "blob request"
```

## 群晖 / OpenWrt / 软路由特殊配置

### 群晖 DSM

群晖 Docker 套件使用独立的 daemon.json：

```bash
# 进入群晖 SSH，编辑 dockerd 配置
sudo vim /var/packages/Docker/etc/dockerd.json
```

添加 registry-mirror 或代理配置（内容同上），然后重启 Docker 套件。

**注意：** 群晖 DSM 7.x 使用自己的 dockerd 配置路径，不是 `/etc/docker/daemon.json`。

### OpenWrt / 软路由

```yaml
# docker-compose.yml（放在 /opt/PullFusion/docker/）
# 注意 OpenWrt 上可能需要映射 /etc/localtime
services:
  PullFusion:
    # ...
    volumes:
      - ./config:/config
      - ./cache:/cache
      - /etc/localtime:/etc/localtime:ro  # 时区同步
```

```bash
# 启动
docker compose up -d

# dockerd 配置（通常在 /etc/docker/daemon.json）
# 使用 CONNECT 代理模式，通过 localhost 访问
```

**软路由特别提示：**
- PullFusion 推荐部署在软路由上，局域网内所有设备共享加速
- 如果软路由内存有限（< 512MB），建议调整 `max_concurrent_global: 16`
- 外挂 U 盘/SATA 硬盘作为缓存目录（`/cache` 映射）

## 节点配置指南

### 添加 socks5 代理节点

编辑 `docker/config/nodes.yaml`：

```yaml
proxies:
  enabled: true  # 必须开启
  nodes:
    - url: socks5://192.168.1.10:1080
      display_name: proxy-ss-1
      priority: 1
      targets: [dockerhub]   # dockerhub 和/或 ghcr

    - url: http://192.168.1.20:7890
      display_name: proxy-http-1
      priority: 2
      targets: [dockerhub, ghcr]
```

**节点类型说明：**

| URL 前缀 | 类型 | 说明 |
|---------|------|------|
| `https://` | Mirror | 公共/私有 Docker Registry Mirror |
| `socks5://` | SOCKS5 代理 | 通过 SOCKS5 隧道访问上游 |
| `http://` | HTTP 代理 | 通过 HTTP CONNECT 隧道 |

**targets 字段：**
- `[dockerhub]`：仅用于 dockerhub 加速
- `[ghcr]`：仅用于 ghcr 加速
- `[dockerhub, ghcr]`：用于所有 registry

### 添加 ghcr Mirror

```yaml
mirrors:
  dockerhub:
    - url: https://docker.1ms.run
      priority: 1
      display_name: 1ms
  ghcr:
    - url: https://ghcr.example.com
      priority: 1
      display_name: ghcr-mirror
      token: "ghp_xxxx"  # GitHub PAT（ghcr 需要鉴权）
```

**ghcr Token 获取：**
1. GitHub → Settings → Developer settings → Personal access tokens
2. 生成 Classic token，勾选 `read:packages`
3. 复制 token 填入配置文件

### 配置热加载

修改 `nodes.yaml` 后，有 3 种方式使其生效：

1. **自动检测**（默认）：配置文件修改后 5 秒内自动重载
2. **手动 API**：`curl -X POST http://<host>:5003/admin/config/reload`
3. **重启服务**：`docker compose restart`

## 自签证书处理

PullFusion 默认生成自签证书，有效期 1 年。

### registry-mirror 模式下的证书问题

修改 `daemon.json` 添加 `insecure-registries`：

```json
{
  "insecure-registries": ["<PullFusion-ip>:5443"]
}
```

或者将 PullFusion 的自签 CA 证书导入系统：

```bash
# 从容器中提取自签证书
docker compose exec PullFusion cat /app/cert.pem > /tmp/PullFusion-ca.pem

# Debian/Ubuntu
sudo cp /tmp/PullFusion-ca.pem /usr/local/share/ca-certificates/PullFusion.crt
sudo update-ca-certificates

# CentOS/RHEL
sudo cp /tmp/PullFusion-ca.pem /etc/pki/ca-trust/source/anchors/
sudo update-ca-trust
```

### 使用自定义证书

```yaml
server:
  tls:
    cert: "/config/server.crt"
    key: "/config/server.key"
```

将证书文件放入 `docker/config/` 目录后重启。

### CONNECT 代理模式 — 无需处理证书

使用 CONNECT 代理模式时，dockerd 直接与上游 registry 通信，PullFusion 作为透明隧道，**无需配置 `insecure-registries` 或导入证书**。推荐使用此模式。

## 升级与回滚

### 升级步骤

```bash
# 1. 拉取最新镜像
cd /opt/PullFusion
git pull

# 2. 重建容器（保留配置和缓存）
cd docker && docker compose down
docker compose build --no-cache
docker compose up -d

# 3. 验证
docker compose logs -f --tail=20
```

### 回滚步骤

```bash
# 1. 切换到指定版本
cd /opt/PullFusion
git checkout v0.1.0

# 2. 重建并启动
cd docker && docker compose down
docker compose build --no-cache
docker compose up -d
```

**注意：** 配置文件和缓存目录通过 volume 挂载，升级不会丢失。

## 网络拓扑图

```
┌──────────────────────────────────────────────────────────────────────────┐
│                           客户端 (docker pull)                            │
└──────────┬─────────────────────────────────────────────┬─────────────────┘
           │                                             │
    方式 A: registry-mirror                       方式 B: CONNECT 代理
           │                                             │
           ▼                                             ▼
┌──────────────────────┐                    ┌──────────────────────────────┐
│  dockerd 直接连接     │                    │  dockerd 通过 HTTPS_PROXY   │
│  PullFusion:5443       │                    │  连接 PullFusion:5003          │
│  (HTTPS, 需配置       │                    │  (HTTP CONNECT 隧道,         │
│   insecure-registry) │                    │   无需修改证书配置)           │
└──────────┬───────────┘                    └────────────┬─────────────────┘
           │                                             │
           └──────────────────┬──────────────────────────┘
                              │
                              ▼
              ┌───────────────────────────────┐
              │         PullFusion         │
              │                               │
              │  ┌─────────────────────────┐  │
              │  │      负载均衡器          │  │
              │  │   (Priority + Speed     │  │
              │  │    + Health + Load)     │  │
              │  └───────────┬─────────────┘  │
              │              │                │
              │    ┌─────────┼────────┐       │
              │    ▼         ▼        ▼       │
              │ ┌──────┐ ┌──────┐ ┌──────┐   │
              │ │Mirror│ │Mirror│ │Mirror│   │
              │ │  1   │ │  2   │ │  3   │   │
              │ └──┬───┘ └──┬───┘ └──┬───┘   │
              │    │         │        │       │
              │    │    ┌────┴────┐   │       │
              │    │    │ Socks5  │   │       │
              │    │    │  代理   │   │       │
              │    │    └────┬────┘   │       │
              │    │         │        │       │
              └────┼─────────┼────────┼───────┘
                   │         │        │
                   ▼         ▼        ▼
            ┌─────────────────────────────────┐
            │    上游 Registry / 代理服务器     │
            │                                 │
            │  docker.io (registry-1.docker.io)│
            │  ghcr.io (ghcr.io)              │
            │  各公共 Mirror                   │
            └─────────────────────────────────┘
```

## 环境变量参考

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `KS_CONFIG` | `/config/nodes.yaml` | 配置文件路径 |
| `KS_REGISTRY_PORT` | `5443` | Registry HTTPS 端口 |
| `KS_PROXY_PORT` | `5003` | 代理 HTTP 端口 |
| `KS_PROXY_AUTH` | (空) | 代理 Basic Auth（格式 `user:pass`） |
| `KS_REGISTRY_DOMAIN` | `registry.local` | 内建 registry 域名 |
| `KS_TLS_CERT` | (空) | 自定义 TLS 证书路径 |
| `KS_TLS_KEY` | (空) | 自定义 TLS 私钥路径 |

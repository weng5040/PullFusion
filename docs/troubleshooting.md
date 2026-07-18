# PullFusion 故障排查

## 常见问题速查

| 问题 | 快速诊断命令 | 对应章节 |
|------|-------------|---------|
| docker pull 报证书错误 | `docker info \| grep -A2 Insecure` | [证书错误](#证书错误) |
| 速度没有提升 | `curl http://<host>:5003/admin/stats` | [速度未提升](#速度未提升) |
| 某个 blob 404 | 查看 `docker compose logs \| grep 404` | [Blob 404](#blob-404) |
| 节点频繁熔断 | `curl http://<host>:5003/admin/nodes \| jq` | [节点熔断](#节点频繁熔断) |
| 内存占用过高 | `docker stats PullFusion` | [内存过高](#内存占用过高) |
| 配置不生效 | `curl -X POST http://<host>:5003/admin/config/reload` | [配置不生效](#配置文件不生效) |

---

## 证书错误

### 症状

```
Error response from daemon: Get "https://192.168.1.100:5443/v2/": 
x509: certificate signed by unknown authority
```

或

```
Error: error pulling image configuration: 
Get https://192.168.1.100:5443/v2/nginx/blobs/xxx: 
x509: certificate signed by unknown authority
```

### 原因

PullFusion 默认使用自签证书，dockerd 不信任自签证书。

### 解决方案

**方案 1：配置 insecure-registries（registry-mirror 模式）**

```json
// /etc/docker/daemon.json
{
  "registry-mirrors": ["https://192.168.1.100:5443"],
  "insecure-registries": ["192.168.1.100:5443"]
}
```

```bash
sudo systemctl restart docker
```

**方案 2：切换为 CONNECT 代理模式（推荐）**

CONNECT 代理模式无需修改证书配置：

```ini
# /etc/systemd/system/docker.service.d/proxy.conf
[Service]
Environment="HTTP_PROXY=http://192.168.1.100:5003"
Environment="HTTPS_PROXY=http://192.168.1.100:5003"
Environment="NO_PROXY=127.0.0.1,localhost"
```

```bash
sudo systemctl daemon-reload
sudo systemctl restart docker
```

**方案 3：导入自签 CA 证书**

```bash
# Debian/Ubuntu
sudo scp user@PullFusion:/opt/PullFusion/docker/config/ca.crt \
  /usr/local/share/ca-certificates/PullFusion.crt
sudo update-ca-certificates

# CentOS/RHEL
sudo cp ca.crt /etc/pki/ca-trust/source/anchors/
sudo update-ca-trust
```

---

## 速度未提升

### 症状

`docker pull` 速度和直连 dockerhub 没有明显区别，甚至更慢。

### 排查步骤

**1. 检查节点健康状态**

```bash
curl -s http://localhost:5003/admin/nodes | python3 -m json.tool
```

确认有多个 healthy=true 的节点，且 `speed_mbps` 不为 0。

**2. 检查是否配置了代理节点**

仅靠公共 mirror 节点速度提升有限。添加 socks5/http 代理节点可显著提升带宽叠加效果：

```yaml
proxies:
  enabled: true
  nodes:
    - url: socks5://192.168.1.10:1080
      display_name: ss-proxy
      priority: 1
      targets: [dockerhub]
```

**3. 检查带宽瓶颈**

- 确认 socks5 代理带宽足够（浏览器下载测试）
- 确认网络延迟（ping 上游 registry）
- 确认系统带宽没有被其他应用占满

**4. 调整并发参数**

```yaml
downloader:
  max_concurrent_per_blob: 8   # 单 blob 并发分块数 (默认 4)
  max_concurrent_global: 128   # 全局并发上限 (默认 64)
  chunk_min_size: 524288      # 最小分块 512KB
  chunk_max_size: 33554432    # 最大分块 32MB
```

**5. 多节点配置示例**

最优配置：1 个低延迟 mirror + N 个 socks5 代理

```yaml
mirrors:
  dockerhub:
    - url: https://docker.1ms.run
      priority: 1

proxies:
  enabled: true
  nodes:
    - url: socks5://proxy1:1080
      priority: 2
      targets: [dockerhub]
    - url: socks5://proxy2:1080
      priority: 2
      targets: [dockerhub]
    - url: socks5://proxy3:1080
      priority: 3
      targets: [dockerhub]
```

---

## Blob 404

### 症状

```
ERROR blob download failed name=library/nginx digest=sha256:xxx error="HTTP 404"
```

### 原因

上游 mirror 资源未完全同步，缺少某个 blob 层。

### 解决方案

**短期：** 修改配置使用不同 priority 的多 mirror 实现故障转移：

```yaml
mirrors:
  dockerhub:
    - url: https://docker.1ms.run
      priority: 1
    - url: https://docker.m.daocloud.io
      priority: 2
    - url: https://dockerproxy.net
      priority: 3
```

当 priority=1 的 mirror 404 时（通过 `fail_count` 触发），自动切换到 priority=2 的 mirror。

**长期：** 配合 khub 搭建私有 mirror，或增加 ghcr 的 fallback。

**手动清理缓存重试：**

```bash
docker compose down
rm -rf docker/cache/*
docker compose up -d
docker pull nginx:latest
```

---

## 节点频繁熔断

### 症状

`curl http://<host>:5003/admin/nodes` 显示大量节点 `healthy: false`，`fail_count` 持续增长。

### 排查步骤

**1. 分析失败原因**

```bash
docker compose logs PullFusion 2>&1 | grep -i "markFailed\|error\|fail" | tail -50
```

常见原因：
- 上游 mirror 不稳定（间歇性 502/503）
- socks5 代理超时
- DNS 解析失败

**2. 调整熔断阈值**

```yaml
downloader:
  node_fail_threshold: 5  # 默认 3，提高容忍度
```

**3. 调整测速间隔**

```yaml
downloader:
  speed_test_interval: 120s  # 默认 300s，加快恢复检测
```

**4. 熔断恢复机制**

- 后台健康探测每 30 秒运行一次
- 测速周期结束后自动对所有节点重测
- 测速通过的节点自动恢复健康状态
- 手动测速：`curl -X POST http://<host>:5003/admin/nodes/<id>/test`

---

## 内存占用过高

### 症状

`docker stats PullFusion` 显示内存持续增长，超过预期。

### 解决方案

**1. 调整全局并发上限**

```yaml
downloader:
  max_concurrent_global: 16   # 从 64 降低到 16
  max_concurrent_per_blob: 2  # 从 4 降低到 2
```

每个活跃下载持有 32KB 缓冲区，64 个并发最大约为 2MB + blob 缓冲区开销。

**2. 限制容器内存**

```yaml
# docker-compose.yml
services:
  PullFusion:
    # ...
    deploy:
      resources:
        limits:
          memory: 512M
        reservations:
          memory: 128M
```

**3. 重启服务**

```bash
docker compose restart
```

**4. 添加 swap（物理机）**

```bash
sudo fallocate -l 2G /swapfile
sudo chmod 600 /swapfile
sudo mkswap /swapfile
sudo swapon /swapfile
```

---

## 配置文件不生效

### 症状

修改 `docker/config/nodes.yaml` 后，服务行为没有变化。

### 排查步骤

**1. 检查自动热加载是否生效**

```bash
docker compose logs PullFusion 2>&1 | grep -i "reload\|watcher"
```

正常输出应包含：`config file changed, reloading`

**2. 检查 YAML 语法**

```bash
docker compose exec PullFusion python3 -c "import yaml; yaml.safe_load(open('/config/nodes.yaml'))" 2>/dev/null || \
  docker compose exec PullFusion cat /config/nodes.yaml
```

**3. 手动触发重载**

```bash
curl -X POST http://localhost:5003/admin/config/reload
```

成功响应：
```json
{"status": "config reloaded"}
```

失败响应会包含具体错误信息。

**4. 检查文件挂载**

确认 docker-compose.yml 中的 volume 映射正确：

```bash
docker compose exec PullFusion ls -la /config/
```

**5. 重启服务（最后手段）**

```bash
docker compose restart
```

---

## 日志查看

### 实时日志

```bash
# 跟踪日志
docker compose logs -f PullFusion

# 最近 100 行
docker compose logs --tail=100 PullFusion

# 带时间戳
docker compose logs -f --timestamps PullFusion
```

### 关键日志模式

| 模式 | 含义 |
|------|------|
| `CONNECT request` | 收到代理隧道连接 |
| `blob request` | 开始 blob 下载 |
| `manifest request` | manifest 查询 |
| `config file changed` | 配置热加载 |
| `node unhealthy` | 节点熔断 |
| `node recovered` | 节点恢复 |
| `blob download failed` | 下载失败 |

### 日志级别

通过环境变量调整（开发模式）：

```yaml
# docker-compose.yml
environment:
  - LOG_LEVEL=debug  # debug/info/warn/error
```

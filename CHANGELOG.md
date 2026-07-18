# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [1.0.0] — 2026-07-19

### 🚀 Features

- **Multi-source Bandwidth Fusion**: 多源并发分块下载，支持 mirror + socks5/http 代理带宽叠加，实测 2~3x 加速
- **Dual Integration Modes**: registry-mirror 模式 + CONNECT 代理模式，灵活适配不同部署场景
- **Smart Load Balancing**: 4 维加权评分算法（Speed / Priority / Health / Load），自动选择最优节点
- **Circuit Breaker**: 连续失败 3 次自动熔断，30s 后台探活 + 定期测速自恢复
- **Blob Local Cache**: LRU 磁盘缓存，二次拉取命中后 10x+ 速度提升
- **GHCR.io Support**: 支持 GitHub Container Registry，PAT token 鉴权
- **Web Dashboard**: 嵌入式 Web 管理面板（/dashboard），实时监控节点状态、下载统计
- **Prometheus Integration**: 内置 8 个核心 Prometheus 指标，可对接 Grafana
- **Multi-arch Build**: 支持 amd64 / arm64 / armv7 多架构构建
- **Config Hot-reload**: fsnotify 自动检测配置文件变更，无需重启

### 🏗️ Architecture

- **Go 1.22** — 标准库 net/http + chi router
- **io.Pipe** — 多源流式合并，零拷贝传输
- **fsnotify** — 配置文件热加载
- **embed** — 嵌入式 Web UI 静态资源
- **自签 TLS** — 内置自签证书生成，1 年有效期

### 📚 Documentation

- `README.md` — 项目概览与快速开始
- `docs/api.md` — 管理 API 完整参考
- `docs/deployment.md` — 部署指南（含群晖 / OpenWrt）
- `docs/troubleshooting.md` — 故障排查手册
- `CHANGELOG.md` — 版本变更日志

### 🛠️ Tooling

- Docker Compose 一键部署
- shell 构建 / 测试脚本
- golangci-lint 代码检查配置
- GitHub Actions CI

---

[1.0.0]: https://github.com/weng5040/kspeeder-lite/releases/tag/v1.0.0

SmartVPN **v0.4.0-alpha.5（纯 Rust + `clash-rs` 独立预览版）** 集中修复了 OPPO Find X8 Ultra（ColorOS 17 / Android 17）真机反馈的 4 项核心问题，并在 Android 17（16 KB 页大小）模拟器上完成 7 大功能项逐项实测验收：

### 本版核心修复
- **订阅拉取红字修复**：补齐 `User-Agent: clash.meta`、301/302 重定向、UTF-8 BOM 与纯公告 `cla=1` 自动重试；Android 端新增显式直连 UDP DNS 解析（`223.5.5.5` / `119.29.29.29`，保留 HTTPS TLS SNI）并允许在 VPN 已连接状态下正常拉取订阅。
- **国内直连列表更新修复**：实现 `POST /api/direct/refresh`、IPv4/IPv6 CIDR 逐行校验、原子写入 `providers/china-ip.txt`、7 天缓存、失败保留旧表与 `SmartVPNChina` 自动挂载。
- **连接后测速与全页接口补齐**：补齐 `/api/latency`、`/api/nodes/chain/probe`、`/api/diagnose`、`/api/tun*`、`/api/path-checks*`、`/api/speedtest`、`/api/events`、`/api/health/params`、`/api/subscriptions/update` 等接口，消除连接后测试失败与页面 404 红字报错。
- **YouTube 等海外站点延迟优化**：强制 `dns.ipv6: false`、补齐 `UDP/443` QUIC 拦截回退 TCP、`redir-host`、DNS 劫持与 MTU 1500，避免在 IPv4-only / 无 UDP 节点上卡超时。
- **真实订阅协议与代理链 UI**：包含 Hysteria2 / Trojan / VLESS URI 兼容修复、服务日志脱敏加固，以及代理链节点选择器实时搜索与按订阅分组筛选 UI。

### 安装包信息
- **Android `arm64-v8a` APK**：`SmartVPN-0.4.0-alpha.5-rust-pure-arm64-v8a.apk`（**16.78 MiB**，`versionCode=44`，16 KB 页对齐，含嵌入式 `clash-lib v0.10.12`，沿用项目调试签名）
- **Windows x64 便携 ZIP**：`SmartVPN-0.4.0-alpha.5-rust-pure-windows-x64.zip`（**27.21 MiB**，纯 Rust 服务 + `clash-rs.exe` + `wintun.dll` + `portable.txt`，零 Go / 零 Mihomo / 零 GPL）

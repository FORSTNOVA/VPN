SmartVPN **v0.4.0-alpha.4（纯 Rust + `clash-rs` 独立预览版）** 基于真实订阅实测与 Android 17（16 KB 页大小）模拟器实测修复了多项兼容性与状态上报问题：

### 本版修复与增强
- **Android 16 KB 真模拟器验证与状态修复**：
  - 在 Android 17（API 37、`PAGE_SIZE=16384`）模拟器上完整验证冷启动、JNI 加载、`VpnService` 授权与 `tun0` 建立、`--wrap=socket` 触发 `CoreBridge.protect`、经 TUN 隧道双向传输、`/api/traffic` + `/api/traffic/history` 记录，以及连续 3 轮连接/断开后 `dropTunnel()` 将 TUN fd 归零（零 fd 泄漏）。
  - 修复 Android 端将连接模式上报为 `tun` 且 `/api/state` 缺少 `tunActive` 导致首页误报检查 Windows `wintun.dll` 的问题，并自动迁移旧 SQLite 配置至 `vpn`。
- **真实订阅协议兼容修复（Hysteria2 / Trojan / VLESS）**：
  - **Hysteria2**：订阅省略 `skip-cert-verify` 时自动补写 `false`（保留证书校验），修复 `clash-rs` 报缺必填字段的问题；
  - **Trojan**：将 `network: tcp` 规范为 `clash-rs` 原生 TCP 表示（保留 WS/gRPC 配置），修复运行时拒启问题；
  - **URI 解析补全**：补全 `flow`、`allowInsecure`、`peer`、`encryption: none` 及无附加报头参数解析；
  - **日志脱敏加固**：Windows 服务日志仅输出固定诊断类别，实测敏感凭证匹配为 `0`。
  - 真实订阅复测通过 **Hysteria2（286 ms）**、**VLESS（325 ms）**、**Trojan（261 ms）** 并发测速与 HTTPS 204 连通性验收。
- **包含 v0.4.0-alpha.3 全部能力**：代理链节点实时搜索与订阅分组筛选 UI、抗宿主代理干扰的订阅全链路、2–8 跳代理链与历史流量 SQLite 持久化。

### 安装包信息
- **Android `arm64-v8a` APK**：`SmartVPN-0.4.0-alpha.4-rust-pure-arm64-v8a.apk`（**16.71 MiB**，`versionCode=43`，16 KB 页对齐，含嵌入式 `clash-lib v0.10.12`，沿用项目调试签名）
- **Windows x64 便携 ZIP**：`SmartVPN-0.4.0-alpha.4-rust-pure-windows-x64.zip`（**27.10 MiB**，含纯 Rust 服务 + `clash-rs.exe` + `wintun.dll` + `portable.txt`，零 Go/Mihomo/GPL）

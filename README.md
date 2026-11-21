SmartVPN **v0.4.0-alpha.2（纯 Rust + `clash-rs` 独立预览版）** 彻底移除了 Go 运行时与嵌入式 Mihomo（GPL-3.0），基于纯 Rust 控制面 + SQLite 持久化 + `Watfaq/clash-rs v0.10.12`（Apache-2.0）构建，体积相比 `v0.3.0-dev.3` 大幅缩减：

- **Android `arm64-v8a` APK**：`SmartVPN-0.4.0-alpha.2-rust-pure-arm64-v8a.apk` — **16.51 MiB**（对比 `v0.3.0-dev.3` 双架构 73.98 MiB 减少 **77.7%**，对比单架构 36.84 MiB 减少 **55.2%**）
  - 仅含 `arm64-v8a` 单架构，内置 `libsmartvpn.so`（含嵌入式 `clash-lib v0.10.12` + `rusqlite` + JNI `CoreBridge` 入口 + `--wrap=socket` 保护钩子）、`libflutter.so`、`libapp.so`，无 Go/Mihomo 符号，全部 4 个 `PT_LOAD` 段满足 16 KB 页对齐。
  - `versionName=0.4.0-alpha.2`、`versionCode=41`，沿用项目调试签名。
- **Windows x64 便携 ZIP**：`SmartVPN-0.4.0-alpha.2-rust-pure-windows-x64.zip` — **25.78 MiB**（对比 `v0.3.0-dev.3` 55.64 MiB 减少 **53.7%**）
  - 内含 Flutter Windows 界面、纯 Rust 本地服务（`smartvpn-service-current.exe` / `smartvpn-rust-service.exe`，非 Go 可执行文件）、`clash-rs.exe`（Apache-2.0）、`wintun.dll` 与 `portable.txt`，不含 `mihomo.exe` 与 GPL-3.0 组件。

### 本版已验收内容
- 本地 SQLite（WAL 模式）持久化设置、订阅源、手动节点与测速记录；
- Flutter 核心 HTTP API（`/api/identify`、`/api/state`、`/api/settings`、`/api/nodes*`、`/api/nodes/latency`、`/api/subscriptions*`、`/api/traffic`、`/api/connect`、`/api/disconnect`、`/api/shutdown`）；
- Windows 便携包解压后独立回环端到端测试通过（含真实 SOCKS5 成员测速、mixed 端口双向传输、`/api/traffic` 速率与连接归一化、Job Object 父进程回收 0 残留）。

> **预览版说明**：本版为 `0.4.0` 纯 Rust 架构早期 Alpha 预览，Android 已完成 APK/签名/16 KB ELF/JNI 符号校验，真机 `VpnService` 流量与远程订阅拉取将在后续 Alpha 中继续完善；日常稳定使用可继续选择 `v0.3.0-dev.3`。

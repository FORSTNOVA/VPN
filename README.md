# SmartVPN (Windows & Android)

跨平台智能分流代理客户端，采用共享 Flutter 控制台界面（响应式适配手机 / 平板 / 桌面），提供 **v0.4.x 纯 Rust + `clash-rs`（Apache-2.0）轻量化预览轨道** 与 **v0.3.x / v0.2.x Mihomo 稳定轨道**。

详细变更记录见 [CHANGELOG.md](CHANGELOG.md)。

---

## 下载

### Windows x64 / Android ARM64 预发布版 v0.5.4-dev.10

[下载 Windows x64 便携 ZIP](https://github.com/FORSTNOVA/VPN/releases/download/v0.5.4-dev.10/SmartVPN-0.5.4-dev.10-rust-pure-windows-x64.zip) · [下载 Android ARM64 APK](https://github.com/FORSTNOVA/VPN/releases/download/v0.5.4-dev.10/SmartVPN-0.5.4-dev.10-release-arm64-v8a.apk) · [发布说明与 SHA-256](https://github.com/FORSTNOVA/VPN/releases/tag/v0.5.4-dev.10)

支持按节点开启 TCP 多路复用（Yamux）或 QUIC 长连接保活。Windows 和 Android 均支持将 Hysteria2 或 TUIC 放在支持 UDP 转发的第一跳之后。

### v0.4.0-alpha.5 纯 Rust 预览版
- **Windows x64 便携 ZIP**：[SmartVPN-0.4.0-alpha.5-rust-pure-windows-x64.zip](https://github.com/FORSTNOVA/VPN/releases/download/v0.4.0-alpha.5/SmartVPN-0.4.0-alpha.5-rust-pure-windows-x64.zip)（~27 MiB）
- **Android ARM64 APK**：[SmartVPN-0.4.0-alpha.5-rust-pure-arm64-v8a.apk](https://github.com/FORSTNOVA/VPN/releases/download/v0.4.0-alpha.5/SmartVPN-0.4.0-alpha.5-rust-pure-arm64-v8a.apk)（~16.7 MiB，支持 Android 16 KB 页对齐）
- **发布说明与 SHA-256 校验**：[v0.4.0-alpha.5 Release](https://github.com/FORSTNOVA/VPN/releases/tag/v0.4.0-alpha.5)

### 传统 Mihomo 轨道
- **v0.3.0-dev.3（预发布）**：[Release 与校验](https://github.com/FORSTNOVA/VPN/releases/tag/v0.3.0-dev.3)（Windows 便携包 / Android APK）
- **v0.2.19（最新稳定版）**：[Release 与校验](https://github.com/FORSTNOVA/VPN/releases/tag/v0.2.19)

---

## 核心特性

- **多订阅与单节点管理**：支持 Clash YAML 与 Base64/URI（`vmess` / `vless` / `trojan` / `ss` / `hysteria2` / `socks5`）订阅拉取、公告伪节点过滤、保底缓存、多源前缀合并、独立刷新 `Subscription-Userinfo` 剩余流量，以及单节点粘贴导入。
- **2–8 跳代理链与真连接测速**：支持自定义多跳代理链（`dialer-proxy`），选节点面板支持**实时搜索**与**按订阅分组筛选/折叠**；支持单节点、前置路径与整链 HTTPS 延迟测速并持久化。
- **智能分流与防泄露**：
  - 自动抓取并缓存国内 IPv4/IPv6 CIDR 地址表（`SmartVPNChina`）实现国内直连；支持「国内加速」与自定义「游戏直连名单」（Windows 按 EXE 路径、Android 按 App UID 排除）。
  - 独立的 **WebRTC 防泄露** 与 **DNS 防泄露**（国内白名单 DoH 直连 + 海外经节点 DoH；支持 `ipv6: false` 与 `UDP/443` QUIC 回退 TCP）。
  - 兼容导入订阅原始分流顺序，并消除与国内加速/防泄露的规则冲突。
- **地区锁与健康巡检**：双源出口地理验证（24h 有效期）、故障驱动同地区自动切换、独立探测防误杀与 Fail-Closed 保护。
- **实时与历史流量统计**：查看实时连接表（进程、目标、命中规则、出站与速率），并以 SQLite 按小时 / 日 / 月 / 年本地持久化历史流量。
- **桌面体验（Windows）**：支持系统代理与 TUN 全设备模式（`wintun.dll` 签名校验下载）、开机登录自启、最小化至托盘、便携目录隔离（`portable.txt` → `SmartVPN-data/`）及可配置的阿米娅 Q 版桌宠（支持右键快捷控 VPN 与全屏自动切换紧凑状态条）。
- **隐私与安全**：本地 API 仅监听 `127.0.0.1` 随机端口并校验 per-run Bearer Token；订阅 URL 凭证自动脱敏不入日志；Android 订阅使用 Android Keystore 加密存储。

---

## 项目结构

| 目录 / 文件 | 说明 |
|---|---|
| `frontend/` | Windows 与 Android 共享 Flutter 界面（含 Windows 托盘与桌宠原生 Runner、Android `VpnService` 桥接） |
| `smartvpn-rust/` | **v0.4.x 纯 Rust + `clash-rs`（Apache-2.0）独立轨道**（纯 Rust 控制面、SQLite 存储、Android 16 KB JNI 嵌入式内核与 Windows 托管服务） |
| `backend/` | v0.2/v0.3 Go 本地服务与 Mihomo 适配层 |
| `backend-rust/` & `backend-rust-store/` | v0.3 过渡期 Rust 核心、HTTP 宿主与 SQLite 存储库 |
| `package.ps1` / `android.ps1` | v0.3 轨道 Windows / Android（默认 `arm64-v8a`）打包脚本 |

---

## 构建与测试

### v0.4.x 纯 Rust 轨道（`smartvpn-rust/`）
```powershell
# Rust 单元测试
. .\smartvpn-rust\build-env.ps1
cargo test --manifest-path smartvpn-rust/Cargo.toml

# 打包 Windows x64 便携 ZIP 与 Android arm64-v8a APK
powershell -ExecutionPolicy Bypass -File smartvpn-rust\package-windows.ps1
powershell -ExecutionPolicy Bypass -File smartvpn-rust\package-android.ps1
```

### v0.3.x 轨道
```powershell
# Windows 便携包 / Android 单架构 arm64-v8a APK（传 -Abi arm64-v8a,x86_64 打双架构）
powershell -ExecutionPolicy Bypass -File .\package.ps1
powershell -ExecutionPolicy Bypass -File .\android.ps1 -Release

# 运行测试
cd backend; & ..\.tools\go\bin\go.exe test ./...
cd ..\frontend; & ..\.tools\flutter\bin\flutter.bat test
```

---

## 许可说明

- **v0.4.x 纯 Rust 轨道**内置 `Watfaq/clash-rs`（**Apache-2.0**），不含 Mihomo 与 GPL-3.0 组件。
- **v0.3.x / v0.2.x 轨道**随包分发 Mihomo（**GPL-3.0**）与 WireGuard `wintun.dll`，完整许可文件位于 `licenses/` 及发行包 `LICENSES/` 目录。

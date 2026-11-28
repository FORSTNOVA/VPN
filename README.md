# SmartVPN (Windows & Android)

SmartVPN 是面向 Windows 和 Android 的智能分流代理客户端，两端共用 Flutter 界面。Windows x64 和 Android ARM64 的 `v0.5.4-dev.8` 预发布版均支持在代理链中按节点开启 TCP 多路复用（Yamux）或 QUIC 长连接保活。Windows 的 Hysteria2 和 TUIC 节点可用作第一跳。

详细变更记录见 [CHANGELOG.md](CHANGELOG.md)。

---

## 下载

### 当前预发布包

- [Windows x64 纯 Rust 便携版 v0.5.4-dev.8](https://github.com/FORSTNOVA/VPN/releases/download/v0.5.4-dev.8/SmartVPN-0.5.4-dev.8-rust-pure-windows-x64.zip)
- [Android ARM64 v0.5.4-dev.8](https://github.com/FORSTNOVA/VPN/releases/download/v0.5.4-dev.8/SmartVPN-0.5.4-dev.8-release-arm64-v8a.apk) · [发布说明与 SHA-256](https://github.com/FORSTNOVA/VPN/releases/tag/v0.5.4-dev.8)
- [Android ARM64 纯 Rust 预览版 v0.4.0-alpha.21](https://github.com/FORSTNOVA/VPN/releases/download/v0.5.4-dev.5/SmartVPN-0.4.0-alpha.21-rust-pure-arm64-v8a.apk)（已有手机安装后启动闪退的反馈）

### v0.5.3 · Android Rust 预发布版（2026-11-25）

国内加速使用可更新的 IPv4/IPv6 地址列表，按目的 IP 对 TCP 连接分流。应用分组指定的节点或代理链优先。开启 DNS 保护时，名单内的国内域名使用国内加密 DNS，其余域名经当前代理解析。代理链每一跳可设置备用节点，前台通知随隧道状态更新。真机验证中，国内网站可经 Wi-Fi 直连，境外 HTTPS 仍偶发连接重置。

- [下载 Android ARM64 APK（17.6 MiB）](https://github.com/FORSTNOVA/VPN/releases/download/v0.5.3/SmartVPN-0.5.3-release-arm64-v8a.apk) · [查看 v0.5.3 发布说明](https://github.com/FORSTNOVA/VPN/releases/tag/v0.5.3)
- SHA-256：`ca45b64a58a770c177aaf45fa0174a777bd3d42baf508a0d06b3566edf473ea3`

### v0.4.0-alpha.5 · 纯 Rust + `clash-rs` 预览版（Windows / Android）

- **Windows x64 便携 ZIP**：[SmartVPN-0.4.0-alpha.5-rust-pure-windows-x64.zip](https://github.com/FORSTNOVA/VPN/releases/download/v0.4.0-alpha.5/SmartVPN-0.4.0-alpha.5-rust-pure-windows-x64.zip)（~27 MiB）
- **Android ARM64 APK**：[SmartVPN-0.4.0-alpha.5-rust-pure-arm64-v8a.apk](https://github.com/FORSTNOVA/VPN/releases/download/v0.4.0-alpha.5/SmartVPN-0.4.0-alpha.5-rust-pure-arm64-v8a.apk)（~16.7 MiB，支持 Android 16 KB 页对齐）
- **发布说明与 SHA-256 校验**：[v0.4.0-alpha.5 Release](https://github.com/FORSTNOVA/VPN/releases/tag/v0.4.0-alpha.5)

### 传统 Mihomo 轨道
- **v0.3.0-dev.3（预发布）**：[Release 与校验](https://github.com/FORSTNOVA/VPN/releases/tag/v0.3.0-dev.3)（Windows 便携包 / Android APK）
- **v0.2.19（稳定版）**：[Release 与校验](https://github.com/FORSTNOVA/VPN/releases/tag/v0.2.19)

---

## 核心特性

- **多订阅与单节点管理**：支持 Clash YAML 与 Base64/URI（`vmess` / `vless` / `trojan` / `ss` / `hysteria2` / `socks5`）订阅拉取、公告伪节点过滤、保底缓存、多源前缀合并、独立刷新 `Subscription-Userinfo` 剩余流量，以及单节点粘贴导入。
- **2–8 跳代理链与真连接测速**：支持自定义多跳代理链（`dialer-proxy`），选节点面板支持**实时搜索**与**按订阅分组筛选/折叠**；支持单节点、前置路径与整链 HTTPS 延迟测速并持久化。
- **智能分流与防泄露**：
  - 自动抓取并缓存国内 IPv4/IPv6 CIDR 地址表（`SmartVPNChina`）实现国内直连；支持「国内加速」与软件分组分流。旧游戏名单升级后迁入「游戏直连」分组；Windows 可按进程名或完整路径匹配，Android 按应用包名在 VPN 内分流。一个软件只能加入一个分组，停用的分组也保留归属；要移动软件，先从原组移除。
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

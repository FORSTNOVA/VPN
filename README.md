SmartVPN **v0.4.0-alpha.3（纯 Rust + `clash-rs` 独立预览版）** 在零 Go / 零 Mihomo（Apache-2.0）纯 Rust 架构上补齐了完整订阅管理、多跳代理链、历史流量持久化与新版代理链节点选择器：

### 核心更新
- **代理链节点选择体验优化（Windows & Android 共享 UI）**：
  - 追加/替换跳节点面板新增**实时搜索**（同时匹配节点名与订阅分组名，忽略大小写，带一键清空与无结果清除筛选）；
  - 新增**按订阅分组横向快捷筛选条**与**分组折叠/展开**（命名订阅源在前、`默认源` 与 `手动节点` 在后，搜索时自动展开命中组，保留单节点测速与前置路径测速）。
- **抗宿主代理干扰的订阅全链路**：
  - 全部 HTTP 客户端显式禁用环境/系统代理，不受本机运行中的其他 VPN/代理干扰；
  - 支持 Clash YAML 与 Base64/URI 订阅拉取、公告伪节点过滤、空/异常响应保底缓存、多源前缀合并、连接中独立刷新 `Subscription-Userinfo` 流量额度及全链路 URL 凭证脱敏。
- **2–8 跳代理链与分流契约对齐**：支持保存/连接 2–8 跳 `dialer-proxy` 代理链；同步落实国内加速、防泄露与导入订阅分流顺序，通过 `clash-rs --compatibility --strict-config -t`。
- **历史流量 SQLite 原子持久化**：后台采样 `/connections` 增量与 checkpoint 同事务入库，支持按小时/日/月/年查询，断开及重启后可查。

### 安装包信息
- **Android `arm64-v8a` APK**：`SmartVPN-0.4.0-alpha.3-rust-pure-arm64-v8a.apk`（**16.70 MiB**，`versionCode=42`，16 KB 页对齐，含嵌入式 `clash-lib v0.10.12`，沿用项目调试签名）
- **Windows x64 便携 ZIP**：`SmartVPN-0.4.0-alpha.3-rust-pure-windows-x64.zip`（**27.09 MiB**，含纯 Rust 服务 + `clash-rs.exe` + `wintun.dll` + `portable.txt`）

> 预览版说明：本版已完成 Windows 独立回环端到端验证（含失效代理抗干扰、两跳链与历史流量持久化）及 Android APK/签名/16 KB ELF/JNI 校验；Android 真机 `VpnService` 实测将在后续版本继续推进。

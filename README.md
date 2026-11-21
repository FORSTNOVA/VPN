SmartVPN 0.3.0-dev.3 修复国内直连加速、WebRTC/DNS 防泄露与导入订阅分流之间的 5 项交互冲突（Go 与 Rust 核心同步更新）：

- **导入订阅分流顺序**：开启「导入订阅分流」且未启用防泄露/国内加速时，`SmartVPNChina` 国内 IP 表改为置于订阅规则之后作为兜底直连，不再抢跑覆盖订阅中的 `REJECT` 广告拦截或 `SmartVPN` 代理规则。
- **国内加速与订阅分流协作**：在防泄露/国内加速模式下，导入订阅的代理规则提前至国内加速之前；开启国内加速时，订阅中合法的 `DIRECT` 规则（`DOMAIN`/`DOMAIN-SUFFIX`/`DOMAIN-KEYWORD`/`IP-CIDR`/`IP-CIDR6`）自动降级为 TCP-only 直连并置于 DNS 与 STUN 保护规则之后，兼顾分流且绝不泄露 UDP/STUN/DNS。
- **DNS 防泄露与国内加速联动**：国内加密 DNS 白名单分流（`nameserver-policy`）改为仅在同时开启「DNS 防泄露」与「国内加速」时生效；仅开 DNS 防泄露且关闭国内加速时，全部域名统一经代理节点加密解析。
- **扩充国内加密 DNS 白名单**：补齐抖音/字节、小红书、拼多多、美团/点评、斗鱼/虎牙、爱奇艺/优酷/芒果、网易、阿里/腾讯 CDN、高德、快手、钉钉、WPS、小米、华为及 `steamcontent.com` 等高频国内后缀。
- **补齐国内 QUIC（UDP/443）回退**：Windows 端浏览器列表补充 `brave.exe`、`opera.exe`、`vivaldi.exe`、`arc.exe`；Android 端在同时开启国内加速与 WebRTC 防泄露时拦截发往国内地址表的 UDP/443 以回退国内直连 TCP（直连名单内游戏已在 VpnService UID 层排除）。

- Windows x64：解压 `SmartVPN-0.3.0-dev.3-portable.zip` 即可运行，默认使用 Rust 核心与 SQLite 存储。
- Android：安装双架构 APK（`arm64-v8a` / `x86_64`），`versionCode` 为 36，沿用项目调试签名。当前 VPN 流量仍由 Mihomo 转发。

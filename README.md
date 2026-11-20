SmartVPN 0.3.0-dev.2 新增开机自动连接，并降低本地 API 的查询延迟。

- Windows x64：解压便携包，在「总览 → 代理模式与分流」中启用「登录时自动连接」。
- Android：安装双架构 APK，首次授权 VPN 后，从同一位置进入系统设置，启用 SmartVPN 的「始终开启 VPN」。

启动时优先恢复上次使用的节点，并遵守已保存的代理链与地区锁。Android 官方模拟器已验证冷启动后恢复所选节点。APK 的 versionCode 为 35，沿用项目调试签名。当前 VPN 流量仍由 Mihomo 转发。

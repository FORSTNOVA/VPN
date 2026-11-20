SmartVPN 0.3.0-dev.1 提供两个独立下载。
 
- Windows x64 用户下载 portable.zip，解压后运行 smartvpn_windows.exe。
- Android 用户下载 APK。它包含 arm64-v8a 和 x86_64 两种架构，versionCode 为 34，沿用项目现有的 Android 调试签名。

两个包均包含实验性 Rust HTTP CONNECT 和 SOCKS5 传输库。当前客户端仍经 Go 平台适配层和 Mihomo 转发 VPN 流量。本次预发布提供二进制快照，标签中附下载文件的 SHA-256 清单。

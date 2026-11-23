pub const TEXT: &[&str] = &[
    "%w（machine 0x%04x）",
    "无法校验 wintun.dll 的签名：%s",
    "%w（签名状态：%s）",
    "%w（签发者：%s）",
    "无法下载 wintun.dll，请检查网络，或在设置中指定本地文件",
    "wintun 下载返回 HTTP %d",
    "wintun 下载中断",
    "%w（压缩包哈希不匹配）",
    "%w（压缩包无法解析）",
    "%w（压缩包读取失败）",
    "%w（dll 哈希不匹配）",
    "%w（压缩包内没有 amd64 库）",
    "无法读取指定的 wintun.dll",
    "已使用你指定的文件",
    "已从官方源安装 wintun %s",
    "无法写入 wintun.dll",
    "无法安装 wintun.dll",
];
pub const BAD: &str = "wintun.dll 未通过校验";
pub const ARCH: &str = "wintun.dll 不是 64 位 Windows 动态库";
pub const VERSION: &str = "0.14.1";
pub const URL: &str = "https://www.wintun.net/builds/wintun-0.14.1.zip";
pub const ZIP_HASH: &str = "07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51";
pub const ENTRY: &str = "wintun/bin/amd64/wintun.dll";
pub const DLL_HASH: &str = "e5da8447dc2c320edc0fc52fa01885c103de8c118481f683643cacc3220dafce";
pub const SIGNER: &str = "WireGuard LLC";
pub const NOT_INSTALLED: &str = "尚未安装";

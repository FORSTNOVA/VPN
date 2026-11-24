# SmartVPN Rust v0.4.0-alpha.3+42

Windows x64 便携版使用 Flutter Runner、本地 Rust 服务和独立 clash-rs 进程。Android arm64-v8a Release APK 在 JNI 库内链接 clash-lib v0.10.12 与 bundled SQLite。Windows 的两个服务文件名对应同一个二进制。Runner 启动 `smartvpn-service-current.exe`，从 stdout 首行读取本次启动的地址、端口与 Bearer token。

## Phase 4C 运行接口

除 `GET /api/identify` 外，所有 HTTP 路由均需本次服务启动时签发的 Bearer token。服务和 Controller 绑定 `127.0.0.1`。订阅下载、额度刷新和 Controller 共用显式禁用代理发现的 ureq 客户端，超时 12 秒，响应体上限 8 MiB。HTTPS 使用 rustls 校验证书。Flutter 在两端的打包副本中将本地服务请求设为 `DIRECT`。

| 路由 | 行为 |
|---|---|
| `GET /api/state`、`GET/PUT /api/settings` | 读取状态与设置，保存订阅 URL。连接期间修改设置返回 409 |
| `GET /api/subscriptions`、`POST /api/subscriptions/add` | 保存带前缀和启用状态的订阅源，读取节点数和独立额度 |
| `POST /api/subscriptions/fetch`、`POST /api/nodes/fetch` | 拉取指定或当前源，发布活动节点。nodes 接口直接返回 Flutter 所需的节点列表 |
| `POST /api/subscriptions/activate`、`POST /api/subscriptions/refresh` | 切换源，或刷新单源缓存。合并模式下刷新成员源后，从各源缓存重新生成合并配置 |
| `POST /api/subscriptions/merge` | 依保存顺序拉取启用源，添加前缀，同名保留首次出现的节点。某源失败时采用该源上次有效缓存并返回警告；缺少有效缓存时返回错误 |
| `POST /api/subscriptions/quota` | 从指定源读取 `Subscription-Userinfo`，只更新其额度和刷新时间，连接期间可用 |
| `GET /api/nodes`、`POST /api/nodes/manual`、`PUT /api/nodes/select` | 列出活动订阅节点与手动节点，支持手动 URI 导入和节点选择 |
| `GET/POST/PUT /api/nodes/chain`、`PUT /api/nodes/chain/profile` | 保存及启停由 2–8 个互异节点组成的链，支持 Flutter 命名链。连接期间修改返回 409 |
| `POST /api/connect`、`POST /api/disconnect`、`POST /api/shutdown` | 空请求体连接使用保存的节点、活动链和分流策略。首跳拨号到下一跳，SmartVPN 组选择末跳 |
| `GET /api/traffic`、`GET /api/traffic/history` | 实时计数及速率，或按本地小时、日、月、年查询 SQLite 汇总。历史接口支持按应用或连接分组 |
| `GET /api/nodes/latency`、`GET /api/nodes/probes` | 经 Controller 测量组成员延迟，按节点保存结果 |

订阅支持 Clash YAML、Base64 和明文 URI 列表。公告节点与空订阅无法覆盖上次有效缓存。活动配置导出至数据目录下的 `providers/subscription.yaml`。订阅 URL 和节点凭证保存在本地数据库，下载错误仅返回状态码或传输错误类型。Windows 使用 `SMARTVPN_DATA_DIR` 或可执行文件旁的 `SmartVPN-data`，Android 使用应用私有目录。

开启 `importPolicy` 后，导入 DOMAIN、DOMAIN-SUFFIX、DOMAIN-KEYWORD、IP-CIDR 与 IP-CIDR6，订阅代理组统一映射到 SmartVPN。导入规则位于国内地址表之前。开启国内加速时，订阅 DIRECT 和国内加速均限定 TCP，DNS 与 STUN 保护优先。国内 DoH 白名单仅在国内加速和 DNS 保护同时开启时生成。地址表随包内置，取自项目既有的 10,019 条 CIDR 快照，域名表包含 70 个后缀。

历史记录按秒读取 Controller 的活动连接。每一批增量和 checkpoint 在一个 SQLite 事务中提交，同一计数重读不会累加，计数回退后从新值开始。查询示例为 `GET /api/traffic/history?granularity=hour&date=2026-11-21T10&group=connection`。日、月、年的日期形式依次为 `2026-11-21`、`2026-11`、`2026`。汇总覆盖采样观察到的字节；完全发生在两个快照之间的短连接可能没有历史明细。

## 构建与验收

在仓库根目录的 PowerShell 执行以下命令。打包使用独立 Cargo home 和指定目标缓存。Android 在 scratch 内使用仓库 Flutter SDK 的副本和独立前端源码副本，传入 arm64、JNI 目录、关闭旧 Rust Core 查找和关闭默认 ABI 过滤的 Gradle 参数。Release 沿用现有 Android 工程配置的 debug 签名。

```powershell
. .\smartvpn-rust\build-env.ps1
cargo test --manifest-path smartvpn-rust/Cargo.toml --target-dir D:\codebuddythink\vpn\.tools\rust-pure-cargo
powershell -ExecutionPolicy Bypass -File smartvpn-rust\package-android.ps1
powershell -ExecutionPolicy Bypass -File smartvpn-rust\package-windows.ps1
python -I -B smartvpn-rust/tests/windows_loopback.py
```

Windows 端从发布 ZIP 解压运行，测试仅使用临时数据目录和回环订阅、SOCKS5、HTTP/TCP 目标。为服务和内核子进程注入指向 `127.0.0.1:1` 的 HTTP、HTTPS 和 ALL_PROXY，同时清空 NO_PROXY。测试预置 GeoIP/GeoSite 数据，核对日志没有数据库下载。生产内核在数据目录缺少这些文件时可能下载数据。应用层直连仍服从操作系统对公网目的地的路由。

## 安装包及验收

| 安装包 | 精确字节数 | SHA-256 |
|---|---:|---|
| [Android arm64-v8a](dist/SmartVPN-0.4.0-alpha.3-rust-pure-arm64-v8a.apk) | 17,506,175 | `0a4b0017d96d036eb5073e481cd7e028a1b406a423dbdd94794fe0dd51d90115` |
| [Windows x64](dist/SmartVPN-0.4.0-alpha.3-rust-pure-windows-x64.zip) | 28,394,013 | `cd5240674732b9a42c801c5369f0f54c1ae81de75716f2115184cb96d60926cb` |

- Cargo 工作区 8 项单测通过，涵盖鉴权、URI 与缓存输入、规则、连接归一化、设置回滚，以及流量计数重放、重置和事务回滚。
- 最终 ZIP 回环验收通过。失效代理下成功拉取 YAML 与 Base64 订阅，保留公告、空响应与 HTTP 503 前的有效缓存；单源刷新更新了合并结果。连接中刷新额度保留了活动配置，错误响应与服务日志通过测试凭证检查。
- 两跳均收到 CONNECT，上传 13 字节、下载 6 字节。断开并重启服务后，四种时间粒度均查到同一连接和相同字节数。八跳配置和四组分流开关组合通过 `--compatibility --strict-config -t`，实际双向传输使用两跳。优雅退出及强杀测试后，clash-rs 与 smartvpn-rust-service 均为 0 个进程。
- APK 清单版本为 0.4.0-alpha.3、构建号 42，v2 签名与 APK 16 KB 对齐检查通过。Windows Runner 的 ProductVersion 为 0.4.0-alpha.3+42。Android 验收限于构建和包体，未做真机接管或公网 TUN 测试。
- 排除 `smartvpn-rust/` 后，仓库 porcelain 状态字节和 Git 可见文件内容的 SHA-256 汇总均与开工基线一致。构建缓存使用获准的 scratch 和 `.tools/rust-pure-cargo`。

APK 只含 arm64-v8a 的 `libsmartvpn.so`、`libflutter.so` 和 `libapp.so`。归档检查使用同版 NDK 复现 Gradle 的 strip 步骤，然后逐字节核对 JNI 文件，验证 AArch64、各 PT_LOAD 的 16 KB 对齐、四个 CoreBridge JNI 导出和 Go/Mihomo 标记。Windows 校验服务别名和 clash-rs 的构建来源，对服务入口运行 `go version -m`，扫描包中 native 文件并核查许可证。Go 工具只用于识别已打包的 PE 文件。


<details>
<summary>Phase 4A 构建与验证记录</summary>

# SmartVPN Rust 0.4.0-alpha.1

构建标识为 `0.4.0-alpha.1+40`。本目录是独立 Cargo workspace，维护自己的锁文件、JNI 库和 Windows 服务。构建入口不调用旧轨道的 Go 后端、Mihomo、打包脚本或 Flutter 工程。

| 位置 | 职责 |
|---|---|
| `crates/smartvpn-runtime` | Loopback HTTP、每次启动从操作系统取得的 256 位 token、Controller 转发、配置生成 |
| `crates/smartvpn-android-jni` | 生成 `libsmartvpn.so`，对接 CoreBridge、VpnService 和进程内的 clash-lib |
| `crates/smartvpn-win-service` | 控制台服务进程、Windows Job Object、外部内核启停 |
| `vendor/clash-rs` | v0.10.12 的 clash-lib、clash-dns 和 clash-netstack 构建源码及上游许可证 |
| `tests` | Android ELF 检查与 Windows 实际内核回环验收 |
| `dist` | 验收后的双端 Release 产物 |

## Android 嵌入实测

NDK 27.2.12479018、API 26、rustc 1.95.0。已检出标签 `v0.10.12`，提交为 `2f62452678211746348b97e72fee0e78b8dc1b40`。Cargo 成功拉取注册表、Git 依赖和子模块。嵌入使用 `default-features = false` 和 `ring,tun`，保留上游 Rustls 与 Tokio-Rustls 补丁的锁定版本。[上游源码与许可证](https://github.com/Watfaq-legacy/clash-rs/tree/2f62452678211746348b97e72fee0e78b8dc1b40)

从父目录指定上游 manifest 裸跑的第一次交叉编译产生了以下错误。

```text
error[E0554]: `#![feature]` may not be used on the stable release channel
 --> clash-lib\src\lib.rs:1:1
  |
1 | #![feature(ip)]
```

上游把 `RUSTC_BOOTSTRAP=1`、`--cfg tokio_unstable` 放在自己的 Cargo 配置中。构建脚本在本次 Android 构建进程中显式传入这两项。未修改全局 Cargo 配置。原版 clash-lib 的 AArch64 release rlib 和独立调用 `start_scaffold` 的 cdylib 链接探针均成功。最终 JNI 库直接调用 `start_scaffold_instance`，包含真实内核。

本地 Android 集成对上游 TUN runner 作了两处行为调整。runner 在实际接收 fd 时复制 JNI 保持的原始 fd，并向控制面发布初始化结果和任务存活状态。JNI 在内核线程退出后释放原始 fd；同一 fd 的 Java 状态刷新复用现有所有权。Android 链接器通过 `--wrap=socket` 包装本库内部的 IPv4/IPv6 socket 创建，内核运行期间通过保存的 JavaVM、CoreBridge 全局类引用调用 `protect(int)`。保护失败会关闭新 socket 并返回错误。停止内核后调用 `dropTunnel()`。

`setTunnel(true, fd, info)` 接收 Java 已分离的 fd。随后用 profile JSON 调用 `POST /api/connect`。只有 TUN runner 初始化并且带密钥的 Controller 就绪检查成功，API 才返回连接成功。JNI 的重复 `start` 在同一服务生命周期内返回同一份 bootstrap。此次 Android 验收覆盖交叉编译和 ELF，真机 VpnService 授权、socket 保护效果及网络切换留待 4B。

## Windows 生命周期与 API

服务先把自身加入带 `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` 的私有 Job，再开放 API。Job 句柄不继承，子进程自创建起即属于同一 Job。stdout 首行输出 `address`、`port`、`token`、`version`、`edition`。客户端连接 `127.0.0.1:<port>`。内核日志使用 stderr。Windows 连接请求使用以下结构，端口和路径由调用者提供。示例选组仅用于本机验收。

```json
{
  "executable": "D:/fixtures/clash-rs.exe",
  "directory": "D:/fixtures/run",
  "config": {
    "mixedPort": 27180,
    "controllerPort": 27181,
    "choices": ["REJECT", "DIRECT"],
    "proxies": [],
    "proxyProviders": {},
    "ruleProviders": {},
    "domesticDirect": false,
    "preventDnsLeaks": false
  }
}
```

`directory` 必须已存在。服务在该目录创建含独立 Controller 密钥的配置，然后启动 `--compatibility -d <dir> -f <generated.yaml>`。正常断开会等待子进程退出并删除该配置。强杀父进程时 Windows 回收内核，工作目录可能仍保留该次配置。Android 的请求体直接使用上例的 `config` 对象。

| 路由 | 行为 |
|---|---|
| `GET /api/identify` | 公开返回 SmartVPN、rust-pure、版本、clash-rs 和内核运行状态 |
| `GET /api/status` | 返回 connected、mixedPort、controllerPort、proxyEnabled、tunnelAuthorized 和 tunnelFd |
| `POST /api/connect`、`POST /api/disconnect` | 创建或释放当前平台内核 |
| `GET /api/proxies`、`PUT /api/proxies/SmartVPN` | 转发内核响应，保留选组的 HTTP 202 和 JSON |
| `GET /api/connections` | 保留连接记录，把 metadata 中的源和目标端口转换为十进制字符串，将网络类型统一为小写 |

除 identify 外均需 `Authorization: Bearer <token>`。监听地址固定为回环，服务 API 使用随机端口。Windows 不设置系统代理，`proxyEnabled` 表示系统代理状态由本服务启用与否。此版本涵盖上表的 Flutter 契约，现有 Flutter 仍走原有轨道。

配置生成器使用 YAML 可读取的 JSON 标量和对象。支持 SOCKS5 节点、`dialerProxy` 到 `dialer-proxy` 的映射、本地文件 proxy-provider、ipcidr/text rule-provider 和 SmartVPN 选择组。国内直连生成 `+.cn` 的标量 nameserver-policy 和域名分流；规则库可承载国内 CIDR。防 DNS 泄漏选项启用代理 DoH，并在普通代理规则前拒绝发往 53/853 的流量。Android TUN 同时开启 DNS 劫持。策略输出由单测核验，未进行整机 DNS 泄漏抓包验收。

## 构建与验收

从仓库根目录在 PowerShell 执行。脚本依赖本机指定 NDK、已安装的 Rust Android 和 MSVC targets、Python 3。测试使用独立 scratch 工作目录、回环源站和指定的内核二进制。

```powershell
. .\smartvpn-rust\build-env.ps1
cargo test --manifest-path smartvpn-rust/Cargo.toml --target-dir D:\codebuddythink\vpn\.tools\rust-pure-cargo
powershell -ExecutionPolicy Bypass -File smartvpn-rust\build-android.ps1
powershell -ExecutionPolicy Bypass -File smartvpn-rust\build-windows.ps1
python -I -B smartvpn-rust/tests/windows_loopback.py
```

四个 Rust 单测覆盖公开识别、状态响应、每次启动换 token、所有私有端点的无 token 与错误 token、TCP/UDP 与端口归一化、配置结构和错误输入。Windows 集成测试向 mixed 入站发起 HTTP CONNECT，在回环目标上双向传输数据，并在连接存活时检查 Controller 记录。测试还核验 202 选组回读、主动断开和强杀父进程两条回收路径。

## 本次验收基线

| Release 产物 | v0.3.0-dev.3 实测字节 | v0.4.0-alpha.1 实测字节 |
|---|---:|---:|
| Android arm64-v8a JNI 库，含内核 | 72,390,512 | 13,599,448 |
| Windows 服务 | 48,853,504 | 970,240 |
| Windows 外部内核 | 61,047,808 | 21,979,648 |
| Windows 服务与内核合计 | 109,901,312 | 22,949,888 |

旧版尺寸取自仓库 `dist/public-v0.3.0-dev.3` 的 APK 和 portable ZIP 条目。新版 Android 的单文件 ZIP 为 **5,853,815 字节**，其中 DEFLATE 数据为 **5,853,661 字节**，压缩级别 9，归档内路径为 `lib/arm64-v8a/libsmartvpn.so`。四个 PT_LOAD 均为 16,384 字节对齐。ELF64 小端、AArch64、四个 JNI 全局函数和 Go/Mihomo 符号检查均通过。动态依赖为 libdl、libm 和 libc。

| 文件 | SHA-256 |
|---|---|
| libsmartvpn.so | `40c91d2ada53c7c8649bb87a847bd98921203e95affe8e488fe158281acb18f1` |
| smartvpn-rust-service.exe | `95a3a8ba527c163aa17c1b7976624f9c66e0c5a543c648da638d2e1f9a514cfe` |
| 测试使用的 clash-rs.exe | `f240565b4f96e1bece427af88b4436149b9858005a2b4e87db943cf0405cb2a0` |

最初用空目录启动 Windows 内核时，一次 Controller 检查超时并报 `os error 10061`。内核日志和上游 CLI 源码确认，兼容模式会补入 Country.mmdb 和 geosite.dat 的默认路径，文件缺失时自动联网下载。最终测试预置有效的 MaxMind 测试库和只含 localhost 的 GeoSite 数据，断言日志中没有下载操作，TUN 与 DNS 监听均关闭。隔离验收连续通过三次，主动断开和强杀父进程后的存活数均为 0。测试用 MMDB 随附许可证。[MaxMind 测试数据来源](https://github.com/maxmind/MaxMind-DB/blob/c454a21f31fd17f2843f16094e9afb3a14a42078/test-data/GeoIP2-Country-Test.mmdb)

</details>

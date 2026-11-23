# Backend migration status and acceptance gates

Version 0.2.20 enables the Rust core, host and SQLite backend in the Windows
and Android distribution scripts. Untagged developer builds and the Go rollback
packages retain their Go implementations.

## Current architectural boundary (Phase 104)

- Flutter UI and Mihomo are retained.
- In Rust-enabled distributions, Rust owns pure calculations, protocol field construction and the
  tested workflow planners listed by phase below: health, settings, subscriptions,
  profile acquisition, quota, connection, kernel lifecycle/exit, region jobs,
  manual nodes, chains, traffic, diagnostic/chain probes, path recovery, path scheduling/control, node queries, lifecycle and source-label recovery.
- The integrated Rust host can serve the public HTTP listener and supervise the
  Windows kernel. Go retains the fallback listener, platform and persistence adapters and remaining task state,
  single I/O/platform effects and codecs. Every migrated planner has a trusted
  Go continuation used for default execution and recovery. This is not yet a
  standalone Rust service or a reduction to a minimal Go platform shim.
- Packaged Rust SQLite opens the existing database with the versioned adapter.
  Go rollback uses the same database and settings files.
- Traffic, path-monitor, health/region/job, ordinary control settings/selection/pins and subscription catalog share a genuine long-lived
  Rust authority. Phase 97 adds full manual URI/proxy records. Remaining profile resources, compatibility backing arrays/maps, and
  standalone HTTP/platform integration remain incomplete. See SERVICE_STATE.md
  PATH_STATE.md, HEALTH_STATE.md, CONTROL_STATE.md and CATALOG_STATE.md for fail-closed and controlled-handoff limits.

## Original implementation and acceptance order

1. Finish the remaining application usecases and concurrency regressions behind
   deterministic fake I/O, including scheduler identity and node queries.
2. Audit startup/shutdown, settings load, platform secrets, codecs, controller
   transport and DNS/UDP/platform boundaries; identify intentionally retained
   Mihomo/OS adapters separately from unfinished self-owned logic.
3. Establish Rust-owned application composition/state and HTTP serving for the
   requested backend replacement; existing workflow DLLs do not do this.
4. Validate Windows and Android packaging/ABI, then use a separate explicitly
   authorized test machine/network for real platform and leak acceptance.
5. Only after those gates consider production backend cutover. Keep Go fallback
   and the currently active user connection untouched during this work.

## Mandatory safety and compatibility gates

- Offline tests use synthetic endpoints and temporary databases/logs only.
- Never run the app/service or the unrestricted Go suite merely to test a pure port.
- No production DLL/executable replacement, real node connection, VPN shutdown,
  DNS/route/proxy/registry changes or credential migration during offline development.
- Check original source SHA256 before applying staged changes; abort on conflict.
- Retain old exports, absent-capability fallback and the default Go build.
- Report source migration, actual bridge execution, packaging, and live acceptance
  separately. No live leak, performance or production safety claim follows from
  passing offline unit tests.

Full-service replacement is not complete. No actual network acceptance was performed.

## Phase 6 working boundary

The Rust HTTP module now participates in exact Bearer authentication, identity JSON
encoding and reachable-node selection from a caller-owned snapshot. Canonical route
planning is tested but does not replace Go ServeMux or business handlers.

Traffic counter deltas, rates and stable row ranking execute through optional Rust
exports. SQLite transactions, background collection, clocks and live kernel queries
remain owned by Go. Out-of-range float conversions deliberately use Go fallback.

The separate `backend-rust-store` crate is a candidate SQLite implementation and is
NOT selected by the production Go service. Cross-language temporary database tests
are necessary but do not authorize running it against an existing user database.

Rust core passes Android ARM64/ARMv7 compile checks only. The Go Android bridge is
still Go; no Android DLL/JNI integration, package installation or device test is
implied. Windows and Android lifecycle/network adapters remain an acceptance gate.

## Phase 8: experimental storage backend

The Go store facade now selects one implementation for its lifetime. A separately
versioned Rust SQLite DLL implements every storage operation, including traffic
history. Its byte-safe ABI preserves TEXT/BLOB reads and caches each operation's
result so output-buffer retries cannot repeat SQL. See backend-rust-store for
configuration, limits and test coverage. This is opt-in Windows amd64 code only;
default Go builds and the running application have not been switched.

Remaining Go ownership includes the HTTP listener/business-handler orchestration,
application state, platform lifecycle, real network probes and connection control.
Compiling Android libraries does not constitute Android Rust integration or live
acceptance. Full backend migration is not yet complete.

## Phase 9: experimental health-service continuation

The successful-patrol, failed-patrol and unblock entry points now use an explicit
Go effects facade. Windows amd64 rustcore builds may opt in with
SMARTVPN_RUST_HEALTH_FLOW=1 and the explicit core DLL. The Rust continuation owns
ordered health-use-case progression; Go retains a trusted expected-effect safety
mirror and deterministic completion after an acknowledged successful switch.
This is not removal of all Go orchestration. See HEALTH_SERVICE.md.

The outer patrol timers, initial bulk query, actual HTTPS probes, platform and
Mihomo operations, and direct failover/block callers outside these three entry
points remain Go. Real network/connection acceptance has not been performed.

## Remaining service migration roadmap (after health continuation)

Completion is measured by owned use cases and integration, not the number of
ported helpers. Substantial Go ownership still remains:

1. Settings update/load/save and subscription-change lifecycle (current work).
2. Subscription registry CRUD/activation, legacy formats and secret wrapping.
3. Subscription download/fallback/refresh/quota, merge/cache and merge scheduling.
4. Connect/disconnect/reconnect, selection and failed-start compensation.
5. Region-verification jobs, region locks and recovery.
6. Chain configuration/probing and manual-node/profile processing.
7. Path monitor, diagnostics, speed/site/STUN/DNS/UDP business orchestration.
8. Traffic collection/history scheduling and HTTP routing/response orchestration.
9. Service lifecycle, single-instance ownership, exit cleanup and log plumbing.
10. Windows/Android platform adapters for Mihomo, proxy/TUN/DNS/relay and secrets.
11. Standalone Rust service entry, Flutter contract parity, packaging and platform
    acceptance. The retained Go default must not be removed prematurely.

Offline adapters and cross-compilation do not authorize modifying live network
settings. Real platform/network acceptance remains a separately controlled gate.

## Phase 10: experimental settings-update continuation

The settings handler now uses a Go service facade and an optional Windows Rust
continuation. It preserves patch presence, validation/override ordering and
non-rollback failure semantics. Node-bound cleanup is decomposed; cache activation
remains a named composite Go effect with partial-state readback. The driver retains
a Go trusted-effect mirror and local completion queue after commit. See
SETTINGS_SERVICE.md and test-settings-offline.ps1. Default Go remains selected;
SMARTVPN_RUST_SETTINGS_FLOW=1 is an additional explicit experiment switch.

## Phase 11: experimental subscription registry lifecycle

The complete 14-operation registry facade now has an experimental Rust planner
with an independently suspended Go safety mirror. It covers load/save/ensure,
find/find-url/list/add/update/rename/use/error/hours/delete/activate. It does not
replace the Go JSON, secret/filesystem/SQLite ports or the composite download,
cache and merge internals. See SUBSCRIPTION_SERVICE.md for bounds and ownership.
Default Go remains selected unless SMARTVPN_RUST_SUBSCRIPTION_FLOW=1 is explicitly
set with the rustcore build and core DLL. No installed/running service was switched.

## Phase 12: experimental subscription acquisition and profile pipeline

The optional profile continuation decomposes cache install/restoration, selected
node protection, retry ordering, fetch/refresh, concurrent acquisition followed
by ordered merge, and auto-merge decisions. Prior registry activation/settings
cache effects now enter this facade. Go retains network/filesystem/platform
adapters and full profile parsing/merge algorithms; quota refresh is still Go.
See PROFILE_SERVICE.md and test-profile-offline.ps1. The additional switch is
SMARTVPN_RUST_PROFILE_FLOW=1; default Go and the running application remain intact.

The earlier roadmap entries are historical: phases 10-12 add experimental
settings, registry and profile workflows, not a standalone Rust HTTP service.
Next is quota refresh with its unlock/request/relock and stale-URL rejection
contract, then connection/selection workflows and the remaining platform/service
boundaries. Offline tests do not establish live network or packaging acceptance.

## Phase 13: experimental quota refresh continuation

The additional SMARTVPN_RUST_QUOTA_FLOW=1 path owns quota refresh retry and commit
planning across unlocked HTTP attempts. Re-locking retrieves current subscriptions
and rejects changed/deleted identities before narrow quota updates. Go retains
actual locks, single GET/header parsing and persistence. See QUOTA_SERVICE.md;
this does not refresh profiles, replace the running service or remove the Go
safety mirror. Connection/selection and remaining platform workflows are next.

## Phase 14: experimental connection/selection workflows

Connect, disconnect/drop and node selection now have the optional
SMARTVPN_RUST_CONNECTION_FLOW=1 planner. Region locking and nested group choices
are decomposed; Go still owns kernel internals, actual platform IO, timers and
patrol cleanup. Default Go and the safety mirror remain. See CONNECTION_SERVICE.md
and test-connection-offline.ps1. No running connection or platform setting was
changed for acceptance. Next is kernel start/stop orchestration, with async
kernel-exit callbacks and platform adapters requiring separate ownership review.

## Phase 15: experimental kernel start/stop workflow

The additional SMARTVPN_RUST_KERNEL_FLOW=1 planner controls start/stop ordering
and compensation with Go-owned runner/relay/config tokens. It keeps original
partial-state and error behavior while closing ABI ownership gaps. See
KERNEL_SERVICE.md and test-kernel-offline.ps1. Platform runner implementations,
real IO and asynchronous kernelExited remain Go; default Go and the safety mirror
are preserved. No real kernel or relay was created during acceptance.

## Phase 16: experimental asynchronous exit cleanup

SMARTVPN_RUST_EXIT_FLOW=1 controls identity-gated kernel-exit cleanup. Runner
identity and locks remain Go-owned, and Windows watcher ordering is unchanged.
See EXIT_SERVICE.md and test-exit-offline.ps1. This is distinct from requested
disconnect/drop; default Go and the safety mirror remain. No live platform
operation was performed for acceptance.

## Phase 17
Region locking and reporting now have opt-in Rust workflow planning. See REGION_SERVICE.md for retained Go boundaries and offline verification. Async region verification remains Go pending the next stage.

## Phase 18
The complete region verification job, including sequential source combination, is now an independently selectable Rust workflow. The start response is frozen under lock before worker scheduling, fixing the prior snapshot race. See REGION_JOB_SERVICE.md for retained Go transport/platform boundaries and validation scope.

## Phase 19
Manual-node lifecycle orchestration now has opt-in Rust planning with original Go graph/slice identity maintained by invocation-local references. See MANUAL_SERVICE.md. URI/JSON/YAML codecs and actual OS/storage operations remain Go ports; existing persisted credential format is unchanged.

## Phase 20
Chain configuration, profile save and pure compilation/build planning now support opt-in Rust. See CHAIN_SERVICE.md. Actual chain probing/dialing and platform execution remain separate Go boundaries.

## Phase 21
Current traffic and history lifecycle/query orchestration support the independent traffic Rust selector. See TRAFFIC_SERVICE.md. Actual timing/select, HTTP/rows and database operations retain explicit Go ports.

## Phase 22
Diagnostic report and multi-source orchestration now have a separate opt-in Rust planner, including explicit concurrent site batch start/join and quality classification. See DIAGNOSTIC_SERVICE.md for retained Go single-probe/parser boundaries and resource-transfer fixes.

## Phase 23
Chain probe and cold/warm measurement orchestration have an independent Rust planner. See CHAIN_PROBE_SERVICE.md for exact compatibility and retained Mihomo/transport/resource boundaries. No real network validation is performed by the offline runner.

## Phase 24
Baseline and recovery path workflows plus legacy rounds and same-region switching now support the independent Rust selector. See PATH_SERVICE.md for preserved quirks, Go probe boundaries and the deliberately separate scheduler work.


## Phase 25
Path monitor scheduling, demand and control workflows now have an independent Rust planner. See PATH_MONITOR_SERVICE.md for explicit double-lock and stale-loop admission fixes, unchanged in-flight semantics and retained platform boundaries.


## Phase 26
Node listing, latency and readiness polling now have an independent Rust planner with explicit borrowed-lock adapters and snapshot ownership. See NODE_QUERY_SERVICE.md for exact merge/transport semantics, the audited adapter fixes and validation limits.


## Phase 27
Service-instance lifecycle and full source-label restoration now have separate Rust planners. See LIFECYCLE_SERVICE.md for resource handoff, partial decode, alias preservation, failure semantics and the remaining Go-owned service/state boundary.


## Phase 28
The traffic baseline and history-error domain can now live in a long-lived Rust authority shared by all traffic usecases, with explicit revision/ack and tested controlled checkpoint handoff. See SERVICE_STATE.md. This is the first authoritative domain, not complete migration of all app state or HTTP serving.


## Phase 29
The complete path-monitor configuration and runtime state domain now shares the long-lived Rust authority with traffic. See PATH_STATE.md for removal of Go state duplicates, generation/lease behavior, v2 capability selection and controlled-handoff limits.


## Phase 33: China Direct

Complete opt-in China Direct authority v6/cap63 and parse/read/download/write/ensure/probe/refresh/status/options workflows. Go retains narrow IO, compatibility parsing/time facts and resource ownership. See CHINA_SERVICE.md. Verified 633 Rust tests, 125 matrix plus 31 golden native workflows, 1800 six-domain authority steps and fault injection; default/actual/disabled/missing DLL integration. Android is compile-only. Speedtest, TUN reporting and remaining HTTP composition are not yet migrated.


## Phase 34: Speed measurement and TUN reporting

Complete opt-in speed handler/measurement/worker/reader workflows and Windows/Android TUN status/check reporting. Narrow resources, codecs and platform observations stay in Go; default Go and non-replaying continuation remain. See SPEED_TUN_SERVICE.md. Independent actual coverage includes 1200 speed cases, 6 speed goldens, nested concurrent native resources, 1600 TUN cases and 14 TUN goldens. Wintun installation, Android descriptor ownership and remaining HTTP composition are not claimed migrated here. Android remains compile-only.


## Phase 35: Application control and Wintun installation workflows

Opt-in application state/health snapshot/parameter/patrol/history/scheduler decisions and Windows Wintun installation/download/archive/architecture/signature workflows. Go retains original resource graphs, mutex/channel/ticker/store/platform facts and narrow IO. See APP_DRIVER_SERVICE.md. No live driver installation or system/network actions are part of verification. Subscription HTTP/policy/diagnostics remain outstanding; default Go and safe native continuation remain.


## Phase 36: Subscription HTTP, scheduling and policy/DNS

Complete opt-in fetch/refresh/diagnostic/payload-analysis and autoMerge scheduler decisions, with policy targets/rules/entryDNS/domain filtering and policy HTTP composition. Narrow Go codecs, resource ownership and original graphs remain. See SUBSCRIPTION_POLICY_SERVICE.md. Bootstrap/ownership/log and adaptive DNS/health-confirmation subflows remain outstanding; this is not yet a standalone Rust host.


## Phase 37: bootstrap ownership and log privacy

Full discovery/join/takeover/locking and platform startup/stop ownership planning,
plus subscription-secret filtering and atomic log scrubbing/writer workflows,
are opt-in Rust sessions. Existing lifecycle children and Go resource ownership
remain explicit. See BOOTSTRAP_PRIVACY.md for raw-byte, continuation, platform and
real-environment limitations. The new offline entrypoint is
`test-bootstrap-privacy-offline.ps1`. Adaptive DNS and independent traffic
confirmation/block orchestration remain next; no production default is changed.
### Remaining composite-workflow audit after Phase 37

The next DNS/health package also includes Android resolver rotation. Further
unmigrated composite decisions remain in converter.go (Sing-Box field projection,
format dispatch, filtering and source merge/renaming/rule rewrites), generic URI
dispatch, subscription URL policy and common profile/source matching. Kernel
readiness retries and Windows/Android privacy/admission decisions also remain.
Those are not merely standard codec/OS boundaries and must not be marked complete
because a parent workflow or individual protocol helper is native. Standard
JSON/YAML/URL codecs, HTTP transport, original graph identities, registry/process/
FD resources and trusted Go continuation remain deliberate compatibility boundaries.

## Phase 38: adaptive DNS, independent confirmation and Android resolver

Complete resolver race/check/monitor/exchange and relay decisions, health
confirmation/anomaly/block orchestration, and Android resolver rotation now use
opt-in planners with trusted Go continuation. See ADAPTIVE_RESOLVER.md. The offline
entrypoint is test-adaptive-resolver-offline.ps1. All actual transports, atomic/
channel/FD/message resources and platform integration stay Go; no production
configuration is switched. Conversion/merging closure and kernel readiness/privacy
admission listed in the audit remain uncompleted application orchestration.
## Phase 39: complete conversion, merge and node frontend closure

The formerly composite converter and profile matching effects now call explicit
conversion/merge/frontend planners. See CONVERTER_NODE.md for standard-codec,
original graph identity, dialect and non-replaying continuation boundaries.
The explicit offline entrypoint is test-converter-node-offline.ps1. Kernel
readiness/ownership/privacy admission remains next; this does not activate native
production code or claim a standalone Rust service or live-device acceptance.
## Phase 40: kernel admission and platform lifecycle decisions

The Windows readiness/ownership/privacy/runner and Android embedded lifecycle
planners close the audited kernel application subchains. See KERNEL_ADMISSION.md
for original ordering, narrow new-command abnormal cleanup and Android partial
failure limits. Full offline entrypoint: test-kernel-admission-offline.ps1.
The final source audit still identifies traffic row/history normalization and
small graph metadata policies. Go hosting/platform/codec resources and actual
native activation/device acceptance are not claimed complete.
## Phase 41: projection and configuration closure

Traffic row/history normalization, manual metadata/domain iteration, proxy graph grouping/current-index, and connection-mode selection now have optional native selectors. Config ABI v2 moves original-rule classification and platform TUN template selection into Rust while preserving v1 compatibility exports. Standard Unicode/path/date/integer/YAML codecs and original graph ownership remain Go. Native config v2 is capability-gated before input construction. Explicit safe aggregate: test-projection-config-offline.ps1. Independent original-source goldens and strict direct-native matrices are required; a fallback result alone is not native acceptance. See PROJECTION_CONFIG.md for scope and limitations.

## Phase 42: HTTP composition, host foundation, Android bridge

All50 Windows/common49 API registrations retain Go ServeMux and original writer/handler lifetime, with opt-in native authorization/dispatch composition before effects. Handler execution is never replayed after an uncertain effect. Existing resource handlers are explicitly Go compatibility adapters, not a completed independent Rust business server.

The host subcrate builds an executable supporting bounded offline framed stdin and exclusive state/resource ownership, once-receipt handling and shutdown drain. It executes synthetic State mutation and identity but returns501 for other host business routes; it does not bind sockets or implement a production HTTP transport/store/restart recovery. Resource panic tests verify no replay and complete state close.

Core bridges use platform shims shared with Android arm64+cgo. Existing Windows files are ignore-tag migration stubs so source-only delivery never leaves duplicate implementations. Android Configure/ConfigureN must be invoked explicitly before service/native use, accepts a bundled absolute core path, and defaults to Go when unconfigured/unavailable. Windows-only native SQLite store ABI remains unchanged. ANDROID_NATIVE_STAGING.md describes compile-only isolated packages, symbol/hash checks, rollback and remaining app/device gates.

Full safe aggregate: test-host-http-offline.ps1. This phase does not enable native production defaults or deploy binaries.

## Phase43: durable offline host and explicit Android startup

Core State::from_checkpoint validates and retains revision, command, ack and pending. The offline host uses a bounded versioned journal with exclusive ownership, durable intent before apply, pending checkpoint, final receipt, and strict restart validation. Unknown outcomes fail closed; the highest damaged record cannot silently downgrade to an older state. Recovery deliberately refuses pending intents and non-default external resource domains. Windows process-restart tests are not proof of power-loss durability; the journal is append-only with no GC and is not the production user database.

Android app source now has a default-false smartvpnRustCoreEnabled build option. The trusted packaged nativeLibraryDir and fixed library name reach ConfigureN via UTF-8 bytes before the original idempotent start. Configuration is consumed at most once; unknown exceptions make subsequent startup fail closed, rather than retry a possibly effective call. Existing JNI start/stop/tunnel symbols remain. Only synthetic Java contracts and native cross-compilation are accepted until separately documented APK/device results.

The aggregate test-durable-startup-offline.ps1 includes the complete previous chain, Rust host disk/restart tests and fresh external Java fake tests. No app, VPN, actual server listener or user database is started by these tests.

## Phase 44: UDP diagnostic reply parsing

The opt-in core parses SOCKS5 UDP reply envelopes and locates the first DNS A
record for the existing relay diagnostic. The shared Windows/Android Go adapter
uses the returned offset in the original packet. Go still owns the sockets,
SOCKS association, DNS transaction matching, timeouts and displayed errors.
Default builds and cores missing the new export use the original Go parsers.

Windows testing covers a loaded DLL, a missing DLL and a default build. The
differential test checks truncated IPv4, IPv6 and domain envelopes, DNS
truncations and byte mutations. Loopback relay tests exercise a forwarded
answer, silence, refusal, a mismatched transaction ID and an unavailable proxy.

## Phase 45: UDP diagnostic request encoding

The optional Windows amd64 and Android arm64 core encodes DNS A questions and
SOCKS5 UDP headers for numeric IPv4, IPv6 and unresolved hostnames. Go provides
the random transaction ID and splits the target address and port using the
existing network-library rules. Invalid UTF-8 in a DNS name takes the Go path
to preserve its rune replacement behavior. A missing request export falls back
independently of the Phase 44 reply parser.

The Windows differential test compares output bytes and errors with the Go
encoders, including maximum-length names, rejected labels and transaction ID
boundaries. The local relay tests exercise the encoders through the probe.

## Phase 46: STUN diagnostic transactions

The UDP egress probe uses the optional Rust binding-request encoder and
matches incoming success replies against the original 12-byte transaction ID.
Go supplies one random ID per request. The address parser still validates the
cookie and attributes after matching, preserving the original selection order.
Phase 45 libraries return unsupported for the new request operation and lack
the matcher export; each operation independently falls back to Go.

The Windows parity test covers 256 transaction IDs, truncated headers and a
mutation of every reply-header byte. A loopback SOCKS5 relay sends a stale
reply followed by a valid reply with a different mapped address. The test
checks the address returned by the normal egress probe.

## Phase 47: SOCKS5 UDP relay address negotiation

The optional Rust core parses the SOCKS5 UDP ASSOCIATE reply and formats the
relay endpoint for the existing Go socket adapter. It requests the exact bytes
needed for the reply and preserves Go's error order, refusal codes, raw domain
bytes, IPv4-mapped IPv6 formatting and loopback substitution for unspecified
addresses. Go retains the TCP handshake writes, deadlines and UDP sockets.
A Phase 46 core or a missing library selects the Go reader before consuming
the reply.

The Windows differential test fragments replies into single-byte reads, checks
each truncation against Go, exercises all 255 nonzero refusal codes and verifies
that a byte following the reply stays in the stream. Local DNS and STUN relay
tests use the ordinary diagnostic entry points.

## Phase 48: SOCKS5 method negotiation and UDP ASSOCIATE request

The optional Rust core builds the initial no-auth greeting, checks the two-byte
method selection and emits the UDP ASSOCIATE request only for version 5 and
method 0. Go performs the stream reads and writes and reports transport errors.
Phase 47 libraries return unsupported for the added encoder operations, so Go
can generate the appropriate frame before the next write on the same connection.

Rust tests exercise all 65,536 two-byte method replies. Windows transcript
tests check the bytes and number of writes on acceptance, rejection, truncated
replies and write errors. DNS and STUN diagnostics also exercise the full
negotiation with a loopback relay.

## Phase 49: DNS transaction selection in the UDP relay

For each datagram received by the DNS relay probe, the optional Rust core
locates the SOCKS5 payload and compares its first two bytes with the query ID
in one call. It returns an offset into the original receive buffer. Malformed
envelopes, short IDs and unrelated transactions leave the probe waiting on
the same socket and deadline. A matching ID ends selection, even when the DNS
parser subsequently reports a malformed response. The Phase 48 core can still
supply envelope parsing while Go performs the ID comparison.

The loopback tests send a fragment, a one-byte payload, a stale transaction
and then a matching reply. A second case places a matching two-byte reply
before a valid answer and checks the DNS parser error. Rust tests cover every
16-bit ID. Windows parity cases cover IPv4, IPv6 and domain envelopes.

## Phase 50: STUN transaction selection in the UDP relay

The STUN egress probe now selects from the complete SOCKS5 datagram in one
optional Rust call. DNS and STUN share envelope extraction. STUN uses the same
success-type and 96-bit transaction check as the Phase 46 matcher. Cookie
and attribute validation still run after selection. A Phase 49 library uses
its existing envelope and STUN matching exports on the same received bytes.

Differential tests check three envelope types, truncated packets and requests,
and mutations of individual bytes. Loopback tests cover stale transactions and
a matching response with a bad cookie followed by a valid response. The five
production migration gates above still apply.

## Phase 51: idle monitoring queries in the Rust host

The standalone offline host answers authenticated traffic, current-node latency
and node-latency queries from its known idle kernel state. Traffic reports
unavailable with the Go reason. The latency queries return their respective 409
messages. HEAD omits the body. The Windows parity test executes the real Rust
host over framed pipes and invokes the three existing Go handlers on an idle
app. Connected queries, controller access and a production HTTP listener remain
open. The default application backend is Go.

## Phase 52: durable replay across handler changes

Disk mode identifies the dispatch rules used to produce an HTTP receipt.
Opening an SVHOST01 checkpoint validates the receipt under the original rules
and replays its exact status and body for the same request ID. A new request
uses the current handlers. A new HTTP receipt uses SVHOST02; earlier binaries
cannot reopen that checkpoint. Records without HTTP receipts retain their
original encoding. Tests exercise exact replay, a new request after upgrade,
reopen and rejection of an altered receipt.

## Phase 53: loopback HTTP listener

The standalone host has an explicit HTTP/1 monitoring mode using Hyper and
Tokio. It binds an OS-assigned 127.0.0.1 port, applies the shared route/auth
policy, and serves GET/HEAD. Offline stdio and durable replay retain their own
entry points and receipt rules.

## Phase 54: attached-controller latency queries

The host can query an explicitly supplied loopback Mihomo controller with
separate authentication. It measures group delays, resolves the selected
outbound including auto/fallback groups, and returns Go-compatible results.
Identity checks controller reachability through its version endpoint.

## Phase 55: connected traffic monitoring

The host reads bounded controller snapshots, serializes rate sampling and
uses the shared Rust projection to rank and cap connection rows. A failed
sample clears the baseline. TestRustHTTPMonitor compares HTTP results with
Go handlers, checks auth and method dispatch, HEAD, idle mode, and controller
error/recovery using a loopback test double. This executable is opt-in.
Production startup, kernel lifecycle ownership and real-device validation
are separate gates. See host/README.md for the launch contract.

## Phases 56–58: Windows supervisor and client attachment

Phase 56 moves Windows kernel creation, exit observation and termination to
the optional Rust host. It assigns itself to a kill-on-close job before
spawning the kernel, publishes the actual kernel PID for port ownership checks,
and treats the parent pipe as its lifetime. Phase 57 attaches the existing Go
client monitor routes to that runner under the existing service authentication.
Phase 58 adds sibling discovery and an optional portable packaging input.
Standalone monitor mode, Go-only Windows packages and Android keep their own
entry points. No host HTTP endpoint can start or stop a system kernel.

Windows lifecycle tests use a synthetic executable with a real loopback TCP
listener and Windows process handles. They check normal stop, supervisor
termination, kernel termination, parent process death, client authentication,
idle handback and bundle discovery. Real Mihomo, TUN, system proxy mutation
and Android device deployment are outside this validation.

## Phases 59–60: Android host attachment and tunnel lifecycle

Phase 59 packages the read-only HTTP host as an optional arm64 Android shared
library. The Rust-enabled JNI service loads it from the selected core's native
library directory. Each session owns its loopback listener, runtime, traffic
baseline and credentials. The Go-only variant continues to handle all queries
locally. The native staging manifest covers all three Rust-variant libraries.

Phase 60 attaches the monitor after embedded Mihomo startup succeeds. The
existing client routes for traffic, selected-node latency and all-node latency
use the session until kernel stop. Explicit disconnect and the VpnService
tunnel-loss path both cancel requests and join the Rust worker before kernel
shutdown. Reconnection allocates a fresh session and token.

Android regression coverage uses an arm64 test binary on an x86_64 Android
emulator with ARM translation, embedded Mihomo with TUN and DNS disabled,
and a loopback fixture controller. It covers failure before attachment,
authenticated queries, restart, normal stop and the Go entry used for tunnel
loss. Installed-APK Java callbacks and traffic through a real Android VPN
remain device acceptance steps.

## Phases 61–62: installed Android VPN acceptance and revoke cleanup

Phase 61 runs a signed arm64 Rust-enabled APK on Android 15 (API 35) with ARM
translation. A separate application UID sends a fresh challenge to a TEST-NET
address through the Android VPN. Embedded Mihomo carries it to a local VLESS
fixture that answers only that destination and challenge. The installed service
also answers a traffic query while its Rust monitor worker is live.

Phase 62 fixes competing cleanup from the Binder revoke callback and main-thread
service destruction. Revocation uses the framework stop, onDestroy releases the
tunnel, and network reports run on the same looper. Releasing a descriptor also
resets the native tunnel permission report. The regression checks ordinary
stop, a second VPN app taking the grant via VpnService.prepare, and traffic on a
new connection after revocation. It checks the production onRevoke log, a single
Java close, absence of app TUN descriptors, worker exit and restoration of the
peer's underlying network. The emulator can retain the previous DOWN interface
while the test process lives. Physical devices and Internet exit throughput are
outside this run. See [the device test](host/android-acceptance/README.md).

## Phase 63: reopen the Android TUN after descriptor reuse

Mihomo's Cleanup closes its listener but keeps LastTunConf. ReCreateTun skips
creation on an equal config, including a recycled descriptor number. The
embedded runner now applies a disabled TUN configuration through the listener
API before Shutdown. This resets the comparison under Mihomo's listener lock.

Four consecutive installed-APK instrumentation runs completed all three
sessions each. Two directly observed consecutive sessions using fd 93 or 92
across revocation, with a successful challenge on the replacement interface.
One earlier invocation of the same final APK aborted in Go GC with
`mspan.sweep: bad span state`. The stack shows allocation during the China
flow; the source of the heap inconsistency is undetermined. This remains an
open stability issue on the translated emulator. The checks used pre-granted
VPN consent and the same per-request HTTP connection lifetime as Flutter.

## Phase 64: Android Go GC crash investigation

The Phase 63 stack stopped during allocation of the China effect-name map.
Every CIDR went through the same map literal. The dispatcher now reads a
package-level indexed table and rejects unknown or reserved operation codes.
This eliminates the repeated map allocation. The stack identifies where Go
detected an inconsistent span; it does not identify what damaged the heap.

The new Android GC regression sends 2,048 CIDRs per parse through the staged
Rust core from three concurrent Go workers, compares every result with the Go
implementation and checks the recovery counter. Three runs per ABI passed on
the Android 15 emulator. Separate Go and cgo allocation controls also passed
on both ABIs. ARM translation remains an open diagnostic variable.

## Phase 65: native Android x86_64 and VPN lifecycle

The Android Rust build and offline staging support matching x86_64 libraries
for Flutter, Dart, Go JNI, the Rust core and the in-process Rust host.
ARM64 remains the packaging default. The APK inspector checks the exact ABI,
presence of both Flutter libraries and byte identity of the SmartVPN libraries.

The acceptance peer starts each challenge in a separate task and waits up to
five seconds for Android to publish the expected default transport to its UID.
Success still requires the nonce reply over TUN, the production onRevoke log,
release of the descriptor and Rust worker, and traffic after reconnect.

On the same Android 15 x86_64 emulator, the native x86_64 APK passed eight
consecutive runs (24 TUN sessions and eight system revocations). The ARM64
APK under Houdini passed three of four runs. Its other process exited with
SIGABRT in Mihomo's DNS hosts lookup; Go reported index 0 out of range for
length 9. This is a second crash signature. The original bad-span signature
did not recur in this series; its root cause and physical ARM64 behavior
remain unverified. The emulator retains the tested native x86_64 APK, with
the temporary peer removed and the client VPN consent reset.

## Phase 66: isolate the translated ARM64 DNS panic

The Phase 65 stack reached Mihomo v1.19.31's static hosts lookup while dialing
a DNS upstream. It failed in the ASCII uppercase test while the hosts mutex
was held. In the exact Phase 65 ARM64 JNI library, LookupStaticHost starts at
ELF virtual address 0xf23680. The saved return PC at +0x30c follows the bounds
panic call. On the ASCII path the compiler compares the length (x2) with the
index (x0) at 0xf237e0 and branches to the panic path at 0xf23814 only when
the unsigned length is less than or equal to the index. Length 9 and index 0
do not satisfy that condition. The stack alone cannot distinguish translation,
corrupt execution state or another runtime interaction.

The Android-only test in `backend/diagnostics/androidhosts` calls the pinned
Mihomo implementation with mixed-case, lowercase and IP-literal inputs from
four concurrent workers under GC. It uses the device's /etc/hosts and does
not start SmartVPN or send DNS packets. The Go 1.24.12 test executables each
passed 30 repetitions (six million calls per ABI) on the same Android 15
emulator, using ARM64 translation and native x86_64 respectively.

The acceptance suite includes a repeated-run driver with per-run Android
timestamps, package hash, UID-filtered runtime logs and exit history on
failure. See [the device test](host/android-acceptance/README.md).

Across 24 full translated ARM64 runs of the Phase 65 APK, 21 passed. The DNS
panic recurred after system revocation at the same +0x30c return PC, this time
with index 0 and length 12. Android recorded SIGABRT for the same PID. Two
other runs failed with a local HTTP EOF during teardown and a local connect
timeout after revocation. Neither failure supplied the DNS panic signature.

## Phase 67: byte-scan comparison on translated ARM64

The diagnostic overlay generator at
`backend/diagnostics/androidhosts/prepare-overlay.ps1` accepts the Go module
cache path and a fresh output directory. It checks the exact upstream SHA-256
and produces a Go `-overlay` file. It changes the ASCII uppercase scan in
Mihomo from iteration over rune offsets to iteration over byte offsets.
ASCII uppercase letters each occupy one byte in UTF-8, and continuation bytes
are outside the ASCII range. The generated ARM64 loop tests its exit condition
before loading the byte; disassembly confirms the separate panic branch from
the original loop is absent.

The experiment replaces the Go JNI library in the signed Phase 65 test APK
and re-signs it with the same acceptance key. The Rust libraries, Flutter
libraries, manifest and DEX payloads match the baseline byte for byte. The
rebuilt Go library also carries local VCS build metadata. The normal packaging
recipe continues to use the pinned upstream dependency.

The byte-scan APK passed 22 of 24 translated lifecycle runs. The other two
failed while launching the peer and waiting for the reconnect HTTP response.
No DNS bounds panic was recorded. In a separate process the byte-scan hosts
test reproduced the original GC fatal error, with state=1, sweepgen=8 and
mheap.sweepgen=8. Changing the scan alone therefore cannot resolve that fault.

## Phase 68: isolate GC behavior across fresh processes

The standalone executable calls only the pinned Mihomo hosts package and Go
test/runtime code. It loads neither the SmartVPN JNI library nor the Rust
core or host. Multiple fresh processes are important: an initial long-lived
process completed 30 test repetitions on each ABI. With ten fresh processes
per ABI (five repetitions each), upstream hosts passed 6/10 on translated
ARM64 and 10/10 on native x86_64. One translated failure reproduced the
bad-span signature; others reported invalid goroutine state or caller PC.

Using the same byte-scan ARM64 executable, twelve pairs of fresh processes
alternated which mode ran first. With default scheduling, 5/12 passed and
three reported bad-span. With `GODEBUG=asyncpreemptoff=1`, 12/12 passed.
Runs shared one emulator and some overlapped application instrumentation.
This associates the failures with asynchronous preemption in this setup.
It does not establish a defect in a specific runtime or translator component.

To repeat the isolated test, build `./diagnostics/androidhosts` from
`backend` with `go test -c`, Android GOOS, the selected GOARCH and the
matching NDK compiler. Copy the executable to the selected device's
`/data/local/tmp`, then run it as a new process with `-test.v -test.count=5`
for each repetition. Set the environment in the Android shell for the
preemption comparison. For the byte-scan variant, add the generated
`-overlay=<path-to-overlay.json>` when compiling.

A separate ARM64 APK retained upstream Mihomo and set the Go runtime default
in the JNI library with `-X=runtime.godebugDefault=asyncpreemptoff=1`
(preserve any existing package defaults when reproducing the build). The
same emulator passed 23/24 lifecycle runs with this APK. All 24 completed
the three TUN challenges, ordinary stop, system revoke and reconnect. One
failed final cleanup: the local HTTP client received an IPv4/UDP DNS packet
before an HTTP status line. Its origin is unresolved; stale writes to a
reused descriptor warrant investigation. Neither the DNS bounds panic nor
bad-span was observed in this APK series. Disabling asynchronous preemption
affects the whole Go runtime and remains a diagnostic build choice.

The emulator was restored to the Phase 65 native x86_64 APK. It passed six
additional runs (18 TUN challenges and six revocations). The temporary peer
and test nodes were removed, the client grant reset and the Android VPN
verified idle. ADB exposed only this Intel emulator; physical ARM64 results
remain open.

## Phase 69: retire Android TUN readers and writers

In sing-tun v0.4.24, LinkEndpointFilter.Attach wrapped a nil dispatcher in
a non-nil filter. The fd-backed gVisor endpoint consequently missed the
detach signal that stops and joins its inbound dispatchers. The direct packet
writer and gVisor batch writer also held the numerical descriptor without
holding an os.File reference during the syscall. Closing that file allows the
OS to assign its number to an unrelated socket. These paths explain how a
late write can reach a recycled descriptor. The Phase 68 trace did not
identify the writer of the individual DNS packet.

The Android overlay forwards nil on detach. Both raw write paths use
syscall.RawConn.Control on the original file, which pins it during a write
and rejects a callback after close. Kernel I/O retains its original packet
and batch handling. The regular Android and offline Rust/Go builds apply
the same overlay, with hashes checked against the pinned dependency.

## Phase 70: Android Studio AVD verification

The Android-only regression checks detach delivery and both real packet write
paths before and after close. It reserves the retired number for a different
writable file and requires both stale writes to fail. Both tests passed 20
times in the official API 36 x86_64 Google APIs AVD.

The installed ARM64 Rust APK (libndk_translation) passed all 25 complete
instrumentation runs on this AVD. There were 75 cross-UID TUN challenges,
25 production revoke callbacks and 25 reconnects that reused the preceding
TUN descriptor number. All cleanup HTTP requests completed. The APK SHA-256
was 818a0bd614d54fc00bbcdcfa612205c86bf1474666843757377b07d11d370bcc.
Both the previous and updated native x86_64 APK exited at startup with
SIGSYS on x86_64 syscall 6 (lstat) under this official image's app policy.
The native x86_64 descriptor test ran under the shell UID; full native
x86_64 application acceptance is still open.

## Phase 71: native x86_64 Android SQLite startup

On the API 36 Android Studio AVD the app process was terminated by SIGSYS
while opening its store. The x86_64 implementation of Xlstat64 in
modernc.org/libc v1.67.6 issues the Linux lstat syscall directly. The
Android amd64 cgo build now uses github.com/mattn/go-sqlite3 v1.14.53,
compiling SQLite with the Android NDK so file operations use Bionic.
The Rust-enabled JNI build and Go rollback build select the same driver.
Other targets continue to use modernc. Database location, schema, WAL
configuration and store operations are shared across drivers.

On the same AVD, the installed ARM64 client seeded a unique loopback node.
After an in-place upgrade, the native x86_64 client loaded and removed it.
A subsequent instrumentation process verified that the deletion persisted.

## Phase 72: native x86_64 app and VPN acceptance

The official Android Studio API 36 Google APIs AVD reported primaryCpuAbi
x86_64 for the installed APK. The first build passed 25/25 lifecycle runs.
The delivery APK added the SQLite driver's license asset; all executable
payloads matched. It cold-started MainActivity, then passed a further 12/12
runs with 36 cross-UID TUN challenge replies, 24 normal stops and 12 system
revocations. Every reconnect carried traffic, including 11 that reused the
previous descriptor number. Teardown checks verified descriptor release,
Rust worker exit and return to the underlying network. The peer and VPN
grant were removed or reset after acceptance. These runs use an on-device
loopback VLESS fixture. Delivery APK SHA-256:
`471247a3d443c53b3378527e727a8175fa875986f15c76161993e7c93b26b68e`.

## Phase 73: Android TUN descriptor ownership

Android grants, kernel handovers and releases now call
`smartvpn_android_tun_lease_v1` in Rust-enabled builds. The Rust transition
preserves ownership on a repeated description of the same fd. On release it
returns a descriptor to close only if the kernel never took it. Go holds the
tunnel mutex, stores the resulting state and performs the requested OS close.
An older core without the export and the Go-only build use the Go transition.
The acceptance runner checks the service log written since each start for
a successful Rust handover of that session's actual fd.

## Phase 74: discard reports from retired tunnel generations

A JNI network query can finish after release and reconnect. Android often
assigns the same fd number to the new connection. The refresh captures the
lease generation under the mutex before calling Java, then applies the result
only if that generation is still active. The descriptor liveness query holds
the same mutex through fstat. On the API 36 AVD, the native bridge and the
older-core fallback each passed 20 repetitions of the ownership and stale
report tests. Coverage includes a repeated grant after handover, release
before and after kernel ownership, duplicate release, and a late report
with the same fd number as the replacement session.

Installed-APK acceptance on the official Android Studio API 36 Google APIs
AVD passed 13/13 runs on native x86_64: 39 cross-UID TUN replies, 26 ordinary
stops and 13 system revocations followed by successful reconnects. Ten of
those reconnects reused the previous fd. The ARM64 APK under
libndk_translation passed 7/8 runs. The failed run completed its first
session, then exited in Mihomo's provider HealthCheck.execute.func1
(healthcheck.go:183, SIGSEGV followed by SIGABRT). The cause remains open.
The x86_64 APK was restored and cold-started, and the AVD's VPN was idle
before shutdown.

## Phase 75: Android provider health checks during VPN turnover

The previous ARM64 APK faulted at ELF offset `0x1ec42a8` in
`HealthCheck.execute.func1+0x88`. The instruction `ldr x3, [x3, #0x70]`
loads the interface method before the call to `p.Name()` on line 183 of
Mihomo v1.19.31. The stack does not establish how the interface became
invalid.

Android builds now use a hash-checked Go overlay for that version.
Health checks take a consistent snapshot of the node list and extra URL
options. Replacing nodes copies the slice, and adding a filter publishes a
new option. The mutex protects configuration and interval reads and is
released before workers start or block on I/O. Workers check cancellation
before beginning another probe. The default URL, filtering, timeout, and
ten-worker limit are preserved. The probe implementation remains in Mihomo.

The focused tests saturate all ten workers, update nodes and filters during
a batch, verify the next batch sees the new configuration, and cancel a
provider with queued probes. Run them from the repository root with
`backend/android-tun/test-healthcheck.ps1 -OutDir .tools/healthcheck-test -Count 50`
using the project's Go toolchain and module cache.

## Phase 76: installed APK lifecycle acceptance

The official Android Studio API 36 Google APIs x86_64 AVD ran both APKs.
Each installed APK hash matched its signed artifact. The Rust core, host,
Flutter and runner came from Phase 73; the rebuilt Go shared library contains
the Phase 75 fix. The series exercised real VpnService callbacks, Rust
handover, cross-UID nonce replies via a loopback VLESS fixture, and cleanup.

| Installed ABI | Full runs passed | TUN replies | Ordinary stops | System revokes followed by reconnect |
| --- | ---: | ---: | ---: | ---: |
| ARM64 under libndk_translation | 24/24 | 72 | 48 | 24 |
| Native x86_64 | 10/10 | 30 | 20 | 10 |

All 34 reconnects reused the previous descriptor number. The client logs
showed no recurrence of the provider crash or bad-span fatal error during
these runs. The native APK cold-started after cleanup, and Android reported
the VPN idle before shutdown. ARM64 hardware was not tested.

Both focused concurrency tests also passed 50 repetitions on Windows and
50 on native Android x86_64. The additional standalone ARM64 Go test binary
exited with SIGSEGV in the AVD command-line translation runner before
printing a test name. Its focused tests are unverified on translated ARM64.

## Phase 77: standalone ARM64 health tests on the official AVD

On the API 36 Google APIs x86_64 image, a translated ARM64 C executable
entered main. A diagnostic wrapper around the original Go test also entered
main, then reported that the ARM64 vDSO supplied in AT_SYSINFO_EHDR had
symbol and hash tables but no DT_VERDEF. With that entry present the Go
executable faulted before listing a test. Ignoring just the vDSO entry in a
diagnostic build let both tests run.

Go 1.24.12 marks this vDSO valid and then dereferences the missing version
definition in vdsoFindVersion. Its ARM64 instruction reads a halfword at
offset 2, matching the reported fault address 0x2. The test build overlay
leaves a vDSO without DT_VERDEF invalid so Go uses its syscall fallback.
The toolchain installation and production APK build inputs for the Go
runtime are untouched. Versioned vDSOs follow the original lookup path.

Use the Android ARM64 cgo environment from package-android-native.ps1 and
run the following from the repository root (with the project Go toolchain
and module cache). Choose a scratch directory for the generated sources.

```powershell
./backend/android-tun/test-healthcheck.ps1 -BuildTranslatedArm64 -GoExe ./.tools/go/bin/go.exe -OutDir .tools/health-arm64
adb -s emulator-5582 push .tools/health-arm64/health-arm64.test /data/local/tmp/smartvpn-health.test
adb -s emulator-5582 shell 'chmod 755 /data/local/tmp/smartvpn-health.test && /data/local/tmp/smartvpn-health.test -test.v -test.count=50'
```

The health snapshot and queued-cancellation tests each passed 110 times
across seven standalone ARM64 process starts on the AVD. The device-side
executable hash matched the build output. Both tests also passed 50 times
each on Windows using the ordinary host test path.

## Phase 78: VPN lifecycle regression after standalone test repair

The same official AVD ran the signed Phase 75 Rust-enabled APKs. The
installed ARM64 and x86_64 APK hashes matched the saved build reports.

| ABI | Full runs passed | Cross-UID TUN replies | Ordinary stops | System revokes and reconnects |
| --- | ---: | ---: | ---: | ---: |
| Translated ARM64 | 12/12 | 36 | 24 | 12 |
| Native x86_64 | 6/6 | 18 | 12 | 6 |

Seventeen of the eighteen reconnects reused the previous TUN descriptor
number. All three sessions in every run returned the expected nonce via
the on-device VLESS peer. The client log scan found no fatal crash during
the series. After the fixture and permissions were cleaned up, the native
client cold-started successfully and Android reported the VPN idle.

## Phase 79: per-app Rust cached-node snapshots

The opt-in Windows and Android cores export `smartvpn_cached_nodes_v1`.
The native cache stores ordered rows, including duplicates, arbitrary string
bytes and all six flags. A replace parses the complete batch before swapping
under its mutex. Updating a source label changes one row. A snapshot returns
owned values and distinguishes a missing cache from a present, empty list.
The Go app mutex orders these operations with subscription and selection
changes. Each app owns its handle. Older cores and default builds use the Go
cache. Disk format and subscription-hash validation are unchanged.

The app retains a Go backing slice for existing settings and profile callers.
A borrowed slice keeps its capacity and element identity. If a caller wrote
through it, the next detached read reconciles those values into Rust under
the app lock. This preserves existing write-through and one-shot settings
intent semantics. It costs an element comparison on borrowed reads and
retains both representations. Go continues to own profile and manual graphs.

## Phase 80: cache-backed queries and concurrency acceptance

The cached branch of the nodes HTTP handler, the connection snapshot, the
manual-node name check and source-label restoration now read through the
cache store. A connected nodes request still asks the controller first.
During cold load, selection flags are applied before publishing the batch.

The concurrency regression uses two writers and three HTTP handler callers.
Each reader verifies both records came from one batch, with the expected
order and source label. On the official API 36 AVD each ABI ran both cache
tests ten times against the new core and ten times against the previous
core with the cache export absent. The Rust ABI regression also races a
reader against replacements, rejects truncated input and checks that a short
output buffer is untouched. The cache disk round-trip fixture now passes
the original settings to the reloaded app, including its subscription hash.

During the first ten translated APK runs, one post-revoke connect got HTTP
502 after the Android interface became visible and before the native
permission was ready. The acceptance runner now waits for both the interface
and a positive authorized native descriptor before calling connect. It makes
one connect attempt per session.

The delivery APKs passed 12/12 complete runs on translated ARM64 and 6/6 on
native x86_64. Across 54 cross-UID TUN nonce replies, 36 ordinary stops and
18 system revocations followed by reconnects, all 18 reconnects reused the
previous descriptor number. The installed APK hashes matched their signed
artifacts. After cleanup the native APK cold-started and Android reported
the VPN idle. The emulator used a local VLESS peer. No ARM64 hardware run
was performed.

## Phase 81: manual-node records

The optional Windows/Android core now holds an ordered, per-app list of
manual node names, types, hosts, ports and creation timestamps. The native
store appends, selects the retained positions on deletion, truncates failed
adds, and replaces whole batches on load and rollback. A missing list and an
empty list have distinct representations. The Go app mutex also orders each
mutation with persistence, the provider file, and its corresponding Proxy
resource. Source URIs and nested Mihomo Proxy maps stay with their original
Go resources. Snapshots match them to native records by ordinal under the
same lock. Changes made through existing borrowed records are reconciled.

## Phase 82: active and named chains

The same store holds the active chain and named profiles. Renaming a profile
removes the old key and inserts the new hop sequence; original hop order and
nil/empty hop slices are preserved. A failed save restores both the original
Go backing objects and the Rust state. Detached consumers receive independent
hop lists. Existing settings callers can still share hop lists or modify the
original profile map; these edits are observed at the next snapshot. The Go
compatibility backing and the native representation are both present in
opt-in builds. Existing settings/SQLite/provider file formats are retained.

## Phase 83: graph-backed queries and Android acceptance

Manual-node listing, the manual portion of the combined nodes query, the
state endpoint's active chain and named profiles, and chain validation now
consume the new snapshots. Profile acquisition, provider metadata, kernel
manual-node counts and chain-enabled policy decisions use the same store.
The default build and older native libraries keep the Go path.

Regression tests use the normal handlers to add and delete synthetic manual
nodes, activate and rename a two-hop chain, read the new name and hop order
via state, and check rollback after a settings write failure. Two writers and
two readers exercise consistent cross-resource snapshots. In the APK series,
each run checks the newly added fixture via both manual and combined HTTP
lists before verifying system VPN consent, tunnel handover and traffic.

On the official API 36 x86_64 AVD, the signed Phase 83 ARM64 APK under
translation passed 12/12 lifecycle runs and the native x86_64 APK passed
6/6. All 54 cross-UID TUN replies, 36 ordinary stops and 18 system revokes
followed by reconnection were verified. All reconnects reused the prior fd.
Installed APK hashes matched the signed artifacts. The three standalone
graph tests each passed ten times on each ABI with the new core and ten
times with the Phase 79 core using the fallback. After fixture cleanup the
native client cold-started successfully and Android reported its VPN idle.

## Phase 84: subscription quota storage

The optional core now stores each catalog quota by its existing resource handle.
Registration, lookup, reference retirement and shutdown share the catalog lock
and lifetime. A successful refresh stores all four signed counters as one value.
Read-only quota responses obtain detached copies. Internal shallow lookups can
still borrow a shared Go pointer, and subsequent reads reconcile its edits.
A library without the quota export continues to use Go storage. On-disk registry
JSON and Android keystore handling keep their existing formats.

## Phase 85: indexed configuration edits and catalog queries

Subscription updates patch the first matching catalog row by ID. Only supplied
configuration fields participate. Listing subscriptions and saving the registry
read detached quota values without projecting health or cached-node state.
Active URL, merge interval and running status are still read from their original
owners. A refresh whose URL changed during fetch returns 409. Windows tests
exercise a database reopen and the established in-memory behavior after a
failed save.

Both signed APKs passed three lifecycle runs each on the official API 36
x86_64 AVD (native x86_64 and translated ARM64). Installed hashes matched.
The six runs included 18 cross-UID TUN replies, 12 ordinary stops and six
system revocations with successful reconnects. The standalone subscription
and quota handler test passed five times for each ABI with the new core and
five times with the Phase 83 core. The cleaned-up client cold-started and
Android reported its VPN idle. All traffic used emulator test fixtures.

## Phase 86: catalog-owned quota values

The production registry now uses the Phase 84 Rust quota store for values
loaded from disk and values reported by profile downloads, including duplicate
subscription IDs. Reading a Rust-owned handle for find-by-ID, find-by-URL,
settings or profile processing never installs a Go pointer in the ledger.
Metadata-only intents skip quota registration. Explicit low-level borrowed
resources retain their compatibility behavior. Earlier cores without the quota
export continue to use Go storage; persisted registry JSON is unchanged.

## Phase 87: refresh and configuration concurrency

A loopback HTTP server blocks its response after a real download begins.
Changing the URL during a quota fetch returns 409 and preserves the earlier
counters and timestamp. A label-only edit and a successful quota response both
survive. Full profile refresh holds the app lock through the download; the
queued URL edit then preserves the downloaded quota and node count.

On the official API 36 x86_64 AVD, native x86_64 and translated ARM64
each passed three signed-APK lifecycle runs. Installed hashes matched;
18 cross-UID TUN challenges, 12 ordinary stops and six system revocations
with reconnection passed. On both ABIs, each of the three concurrency cases
passed five times with the current core and five times with the Phase 83 core
using Go quota storage (60 cases total). These tests used loopback providers.

## Phase 88: remove the last explicit UserInfo borrow

The remaining borrow flag was reached through low-level catalog restore and
single-row patch helpers used by the authority and fault-injection tests.
Restore now copies each non-nil input into the existing Rust quota store.
A quota patch replaces its row's handle; a metadata-only patch keeps the
handle and allocates no quota. Rust reads always return detached values,
including after rollback or same-process handoff. Go tracks live native
handles for checkpoint validation and retirement. Only a core missing the
quota export allocates the pointer-to-handle table and preserves shared reads.

The restore/patch regression passes a single input pointer to two rows,
replaces one quota, applies a label edit, and rolls back a deletion. It checks
value isolation in native mode and pointer identity in fallback mode. Windows
coverage also exercises pending writes and retirement under injected faults.

On the official API 36 AVD, the signed native x86_64 and translated ARM64
APKs each passed three lifecycle runs, with 18 cross-UID TUN replies,
12 ordinary stops and six successful reconnects after system revocation.
Installed hashes matched. Per ABI, the new restore/patch test and all three
refresh/edit interleavings passed five times with the current core and five
times with the Phase 83 fallback core.

## Phase 89: subscription response usage headers

Profile downloads and quota-only requests now share the Rust parser for
Subscription-Userinfo. Operation 7 of the subscription ABI returns the four
signed counters, or an empty result for an absent/unparseable header.
An unknown numeric key still marks a header present; later valid repeated keys
win. Unsupported operations and invalid UTF-8 use the existing Go parser.
HTTP transport, authentication-bearing URL handling, timeouts, response limits
and parsing proxy objects from the body remain at their existing boundaries.

## Phase 90: rule targets in merged profiles

Rust identifies the target field of each rule, preserving byte offsets, the
optional trailing no-resolve flag and reserved DIRECT/REJECT targets. The
native converter planner and subscription operation 8 reuse the same scanner.
The Go merge executor uses these bounds with its per-source alias table and
keeps the Go scanner for older cores. YAML resource assembly and serialization
still run in Go.

The Windows live merge test ran against the portable Mihomo v1.19.31 binary.
With the watcher and the fixture using the same lock at shutdown, five runs
each in Rust-enabled and default Go modes preserved the selected original
node and start time, then successfully selected the newly merged node.
All proxy endpoints in this test were loopback fixtures.

The official API 36 x86_64 AVD passed three signed-APK lifecycle runs per ABI
(native x86_64 and translated ARM64). Installed hashes matched. There were
18 cross-UID TUN replies, 12 ordinary stops and six system revocations with
successful reconnection. On both ABIs the download/merge regression passed
five times with each of the current, Phase 88 and Phase 83 cores. The tests
also checked that only the current core accepted the new operations.

## Phase 91: Clash YAML subscription objects

Subscription operation 11 decodes the raw proxy mappings, including nested
Mihomo extensions. Operation 9 projects selectable nodes, preserving source
labels, first-name ordering, notice filtering, TLS and WebSocket metadata.
The two outputs are distinct: a repeated name may occur in the raw list but
only its first eligible occurrence appears in the selectable list. Integer
fields cross the Go bridge without a float64 conversion.

The native parser accepts a conservative subset. Profiles containing alias,
merge or tag markers and ambiguous numeric/date spellings use the existing
Go decoder. Non-string mapping keys and unsupported scalar types also fall
back during raw mapping conversion. The existing subscription ABI size limits
apply. URI-specific and sing-box JSON adapters retain their previous paths.

## Phase 92: merged YAML serialization

Operation 10 serializes the merged document in Rust. The output is block YAML
at the top level with flow-style values; each sequence item has its own line.
Quoted nested strings preserve names, credentials, timestamps-as-text and
control characters. The bridge carries inline group extensions, source
sections, DNS policy, group members/providers and rule ordering. Open-ended
values outside the lossless bridge (such as typed timestamps and floats)
go through the Go YAML marshaler. Old libraries that reject the new operation
numbers use the existing Go codecs. The single-source passthrough and the
separate YAML policy decoder retain their previous behavior.

The locked dependencies include serde_yaml_ng and serde_json. For an empty
Cargo cache, run `cargo fetch --locked --manifest-path backend-rust/Cargo.toml`
before the offline build. This run used a project-local Cargo cache under
`.tools/phase91/cargo-home`.

The installed Windows Mihomo v1.19.31 executed the live merge five times in
each of the native and default Go modes, and three times with the Phase 89
DLL. Each run kept the original node and running kernel, then selected the
new node. On the official Android Studio API 36 AVD, the native x86_64 and
translated ARM64 APKs passed three runs each: 18 cross-UID TUN replies,
12 ordinary stops and six system revocations with successful reconnection.
Installed APK hashes matched. On each ABI, codec parity and loopback
download/merge each passed five times with each of the current, Phase 89 and
Phase 83 cores. All 90 refresh/edit interleaving cases passed.

## Phase 93: YAML anchors and merge keys

The subscription node query and proxy-object reader now consume YAML parser
events. Aliases reference previously completed nodes. An explicit mapping
field overrides inherited fields, and earlier mappings in a merge sequence
take precedence. A quoted `"<<"` remains an ordinary key. String fields
such as node names and source labels retain their original scalar spelling.
Cycles, invalid merges, explicit tags, unsupported key types and inputs above
the expansion budget return control to the Go decoder.

## Phase 94: YAML numeric resolution and round trips

The object reader handles legacy and prefixed octal, binary and hexadecimal
integers, decimal floats, unsigned 64-bit values, negative zero, infinities
and NaN. Regression tests compare decoded Go types and float values before
and after serializing the merged configuration. Operation 13 carries
non-finite values across JSON. Operation 12 emits floating-point tokens
formatted by the Go bridge with the same precision as yaml.v3. User mappings
that collide with the private token marker use the Go codec. Hosts still
accept Phase 91 operations 9–11 and fall back to Go with older cores.
Raw timestamps remain on the Go path. The separate policy decoder is unchanged.

The Windows Mihomo regression loaded a loopback subscription whose proxy
inherits an octal port, kept the selected node and process start time, and
selected both imported nodes. The repeat suite passed five runs each with
the current core and default Go, plus three with the Phase 91 DLL.

On the Android Studio API 36 AVD, each ABI (native x86_64 and translated
ARM64) passed three lifecycle runs with matching installed APK hashes. Across
both ABIs the runner observed 18 cross-UID TUN replies, 12 ordinary stops
and six system revocations followed by successful reconnects. Each ABI also
passed five codec-suite repetitions in each of the current, Phase 91 and
Phase 83 core modes. Evidence is under `.tools/phase93/`. In particular,
`lifecycle-summary.json` links the lifecycle counts to the two APK hashes.

## Phase 95: subscription groups, rules and DNS policy

Subscription operation 14 projects proxy groups (including inline extension
fields), ordered rules, merged source rules, per-source policy sections, proxy
names and hosts, and DNS nameserver-policy values. The typed fields preserve
scalar spelling and the distinction between missing and empty lists. The
merge uses one projection per source for both entry DNS and policy. Resolver
eligibility, group target selection, rule translation and Mihomo hot reload
continue through the existing business flows.

Kernel configuration and the policy preview share a per-app, single-document
cache keyed by the SHA-256 digest of the profile contents. The preview still
checks subscription identity and file-read errors on each request. A changed
file at the same path gets a fresh projection. Unsupported documents and old
libraries use the caller's original Go decoder. DNS and rule projections can
therefore still succeed independently when a different section is malformed.

The local Mihomo repeat suite passed five times each with Rust and default
Go, and three times with the Phase 94 core. It checked the live controller's
imported rule, the preview, the merged entry DNS mapping, node selection and
the original process start time. On the Android Studio API 36 AVD, native
x86_64 and translated ARM64 each passed three lifecycle runs. Together they
produced 18 cross-UID TUN replies, 12 ordinary stops and six system revocations
followed by successful reconnections. Each ABI passed five policy parity runs
with each of the current, Phase 94 and Phase 83 libraries. APK hashes and
lifecycle counts are recorded in `.tools/phase95/lifecycle-summary.json`.

## Phase 96: source labels and query metadata

The node query now projects ordinary Clash profiles directly through Rust
operation 15. JSON inputs retain the converter's sing-box precedence; URI,
base64, ambiguous typed scalars, and older cores use the existing converter.
Operation 16 decodes the label-recovery document. The recovery workflow still
checks the active proxy graph before adding missing metadata, and retains
the same atomic file replacement and cache-write ordering. Chain and metadata
index consumers share the typed proxy-object reader and keep all duplicates,
including manual map identity.

Operation 17 builds the manual metadata overlay from typed facts, preserving
last-name-wins behavior and WS header presence. Operation 18 computes label
and selection changes for existing node rows; the host writes the original
backing slice, preserving borrowers. Manual URIs and credential maps stay
with the existing resource owner.

When a subscription contains repeated proxy names, the converter can return
fewer summaries than proxy objects. Publishing a profile now reconstructs
the affected summaries from the final unique names and keeps unaffected
sources' dialect-specific metadata. The low-level converter API retains
its positional compatibility behavior. Startup and live node queries use
the same published names and labels. The on-disk formats are unchanged.

Windows Mihomo passed five repeated merges with the new DLL, five with the
default Go build and three with the Phase 95 DLL. Each run checked connected
queries after a naming collision, renamed a source, merged again, then
started a fresh backend test process against the same temporary store and
profile cache and checked the idle query. The live policy rule, DNS mapping,
current selection and kernel process start time were also checked.

The Android Studio API 36 AVD passed three lifecycle runs on native x86_64
and three on translated ARM64: 18 cross-UID TUN replies, 12 ordinary stops
and six revocations followed by reconnection. The metadata and refresh/reopen
tests passed five repetitions per ABI for each of the Phase 96, 95 and 83
libraries. Artifact hashes and counts are in
`.tools/phase96/lifecycle-summary.json`.

## Phase 97: full manual-node records

The per-app Rust graph now stores the original URI and nested proxy payload
with the manual node's metadata in the same ordered record. The full-record
capability has its own version probe. The older graph ABI still exposes its
metadata-only view. Current hosts retain the Phase 81 scalar/Go-resource path
with older libraries and the default Go path when no graph is available.

Node queries, persistence and provider generation take full snapshots under
the app lock. String lists, heterogeneous lists, nested maps, typed nils,
integers and reloaded JSON float64 values retain their types. Query snapshots
of supported records are detached. Compatibility callers can still mutate
an added node's original proxy map under the app lock. Per-row fingerprints
detect those edits so only the affected row is republished. Runtime-only
proxy values outside the codec and values beyond its nesting limit keep
their Go adapter. Node-list HTTP responses expose the existing public fields.

A failed add truncates the appended row. A delete retains its original
Rust records until the save completes, so failure restores both the native
payloads and the original Go backing references. SQLite JSON and provider
YAML retain the existing formats. A provider-write failure after a successful
database save retains the existing service behavior.

The local Windows Mihomo test added a synthetic VLESS/WebSocket node,
reloaded it, selected it and verified an HTTP response over its manually
configured provider. The loopback fixture checked the host, path and VLESS
request. The repeat suite passed five times with the current DLL, five
with default Go and three with the Phase 96 DLL. On the Android Studio API
36 AVD, both native x86_64 and translated ARM64 passed three lifecycle runs:
18 cross-UID TUN replies, 12 ordinary stops and six revocations followed by
reconnection. Full-record parity, save-failure rollback, concurrent reads
and SQLite reopen passed five times per ABI and core mode (97, 96, 83).
Installed APK hashes and run counts are in
`.tools/phase97/lifecycle-summary.json`.


## Phase 98 — active and named chain transactions

With the chain transaction capability, the per-app Rust graph owns the active
chain and named profiles. Updating the active chain and renaming its profile
share one undo record and the app lock through persistence. Go installs the
completed settings document by renaming a temporary file in the same directory.
On failure Rust restores both records and Go restores the original borrowed
map and hop slices. Compatibility views track their last published contents
so a stale view cannot overwrite a native update during a query or save.
Loading settings, legacy chain migration and health updates use the same graph
write adapter. Cores without the new export retain their previous write and
rollback behavior. The running-kernel and region-job guards still apply.

Windows tests compared active-profile rename, switching, concurrent edits,
disk reload, and a failed save against Go. A local Mihomo process exercised
two separate loopback HTTP first hops and a shared exit. While running it
rejected an active-chain edit with HTTP status 409. After stopping, switching
and restarting, a controller delay probe traversed the second path. The live
repeat suite passed five times on the Phase 98 DLL, five on default Go and
three on the Phase 97 DLL.

On the official Android Studio API 36 AVD, the native x86_64 and translated
ARM64 APKs each passed three lifecycle runs. The six runs produced 18 cross-UID
TUN challenge replies, 12 ordinary stops and six system revocations followed
by successful reconnection. Separate chain transaction tests passed five
times for each ABI with each of the Phase 98, 97 and 83 libraries. Installed
APK hashes and per-ABI counts are in `.tools/phase98/lifecycle-summary.json`.


## Phase 99 — complete read-only HTTP ingress

The opt-in service routes all 16 GET paths and their HEAD variants to the
Rust host in both idle and connected states. This includes identity, state,
subscription policy, diagnostics, subscriptions, nodes, both latency
queries, path checks, traffic and its history, manual nodes, probe results,
regions, events and TUN status. The public address and write routes retain
the existing Go service lifetime. Rust authenticates and selects a read
operation before contacting the private resource adapter and streams the
result, including error responses. The app, SQLite and controller continue
to provide live data through the existing handlers. A missing capability
or unavailable host at startup selects the Go path. A request already sent
to a running host is never replayed through fallback after an I/O error.

The Windows route matrix compares status, content type and response bodies
for GET and HEAD, with missing, invalid and valid credentials. Each route
runs idle and connected to local Mihomo. Additional checks exercise query
parameters, an unknown path and a method mismatch. Current Rust, the Phase
98 DLL and the default Go build passed. The Rust host test checks that an
unauthorized read and a write cannot invoke the resource adapter and that
the client bearer never reaches it.

On the Android Studio API 36 AVD the final installed APK hash and ABI were
verified before each series. Native x86_64 and translated ARM64 each passed
three runs: 18 real cross-UID TUN replies, 12 ordinary stops, and six system
revocations followed by reconnect. Instrumentation verified that the read
worker served idle state and stayed alive after the per-kernel monitor was
released. The per-ABI hashes and lifecycle results are in
`.tools/phase99/lifecycle-summary.json`.

## Phase 100 — integrated write API and service dispatch

The public mux, the capability fallback and the private effect adapter share
one route registry. A current host handles all 50 Windows routes (34 writes)
and all 49 Android routes (33 writes). The Windows driver operation is
platform-specific. The Rust host chooses the authenticated numeric effect
before invocation. It drains the entire write request and retains only the
bytes visible to the existing Go JSON parser. It preserves method, query,
content type and response status. A Phase 99 host or core can still serve
reads while Go handles writes. Startup without either host uses Go.

Settings updates stage the candidate document before publishing catalog and
cache changes. Failed catalog edits restore the edited fields. Activation
of a single subscription preserves previous active files and delays health
and region cleanup until success. Manual edits restore the previous records
when provider publication fails. A rejected chain transaction keeps its
existing rollback. A failed node selection restores its prior group and
settings. Connect startup retains its previous settings and cache during
the transition and defers the node upsert until the proxy step succeeds.
An acknowledgement lost after an operation reached the adapter remains an
uncertain client outcome. No handler is invoked a second time.

The write suite exercises authenticated client requests, denied writes,
settings and SQLite failures, catalog and manual-node edits, activation
failure, chain edits, live Mihomo group switching and disconnect. It checks
controller, memory and persisted state after failed operations. A separate
post-start cache-save failure checks that the kernel stops and previous
selection and files are restored. Windows coverage includes default Go,
Phase 99 core, Phase 99 host and absent host. Three additional runs enabled
the Rust business planners. The host transport regression covers a truncated
upload and loss of the adapter reply after receiving a chunked POST.

The official Android Studio API 36 emulator ran the final native x86_64
and translated ARM64 APKs three times each. The runner checked the integrated
API worker, issued real manual-node, selection, connect and disconnect
requests, and verified 18 cross-UID TUN replies, 12 ordinary stops and six
system revocations followed by reconnect. Installed SHA-256 and per-ABI
counts are in `.tools/phase100/lifecycle-summary.json`.

## Phase 101 — Windows kernel and platform orchestration

On Windows the new core capability enables the Rust connection, kernel,
admission, exit and proxy workflows in a Rust-enabled service. The supervisor
and launcher negotiate managed reload at startup. Each kernel generation
has a separate write credential and forwards config updates to its own
Mihomo controller. Older supervisors and cores keep the existing controller
path. A reload error after negotiation is returned without replay.

On unexpected exit the matching runner stops its path monitor and releases
the generated config. Proxy restoration compares the five WinINET values to
what SmartVPN applied. The platform adapter rechecks each value before writing
and preserves intervening external changes. A failed restore leaves the
backup and the pending proxy flag available to a subsequent disconnect.

Windows acceptance used the installed Mihomo, temporary homes, loopback
listeners and a dedicated HKCU test key with notification broadcasts disabled.
`TestLiveWindowsKernelGeneration` exercises rejected reload, forced child
exit, cleanup, restart, stale callbacks and disconnect/restore retry.
`TestLiveSubscriptionMergeThroughMihomo` checks two subscription merges on a
running controller. Both passed with the current pair, Phase 100 DLL, Phase
100 supervisor and default Go. The live system proxy and host VPN were left
in place. Per-mode logs are in `.tools/phase101/live-*.log`.

## Phase 102 — Android kernel and VpnService orchestration

Tunnel grant and revocation now acquire the current app lock before changing
descriptor ownership. Permission update, the running check and revoke cleanup
share the same critical section as connection start and subscription reload.
The Rust embedded session starts the monitor after kernel admission and stops
it before kernel shutdown. Live reload checks the kernel's tunnel generation,
then validates that the parsed configuration retains the active descriptor.
Java owns system callbacks and the Go adapter invokes Mihomo and JNI. The
Go continuation implements the same transitions for older cores.

On the official Android Studio API 36 AVD, native x86_64 and translated
arm64-v8a each passed three series. Together they exercised 18 live subscription
merges followed by selection of the merged node and a cross-UID TUN reply,
12 ordinary stops and six system revocations followed by reconnect. Four
of those reconnects reused the previous numeric descriptor. A separate
x86_64 APK with the Phase 100 core passed the same three-session sequence.
The descriptor-reuse tests also ran with Rust enabled and with the Go fallback.
Installed APK hashes and results are in `.tools/phase102/lifecycle-summary.json`
and `.tools/phase102/legacy/fallback`. All VPN changes occurred in the AVD.

## Phase 103 — persistence and old-profile compatibility

Rust-enabled Windows amd64 and Android x86_64/arm64 builds can open the existing
`smartvpn.db` through a versioned Rust SQLite library. Android native staging
accepts `-IncludeRustStore`. Existing packages without the library use Go.
Selection occurs before opening the database, once per store. The Go and Rust
open paths apply schema changes in one transaction. Settings and named chains
continue to use the existing settings document alongside the database.

Windows tests exercised an old Go-created database copy, explicit rollback,
node timestamps, switch history and reading Rust writes from Go. A subprocess
flushed the uncommitted legacy-schema upgrade and exited before commit.
Reopening confirmed the old schema was intact and the upgrade succeeded on retry.

The official API 36 AVD ran Phase 102 → 103 → 102 in place with each ABI.
Client queries checked settings, subscriptions, manual nodes, named chain hops
and a synthetic old-format traffic row. The old APK read the catalog and setting
updates made by the Rust-enabled APK. Each Phase 103 APK also passed ordinary
stop, system revocation, reconnect and cross-UID TUN traffic. APK hashes and
per-step instrumentation logs are in `.tools/phase103`.

## Phase 104 — distribution defaults and rollback

Version 0.2.20+33 ships a Windows x64 portable backend and an Android APK
with arm64-v8a and x86_64 libraries. Both distribution scripts select Rust
by default. Pass `-BackendMode Go` to build the matching rollback artifact.
On Windows, `SMARTVPN_BACKEND=go` selects Go at service startup even with
all Rust libraries installed. Stage a portable upgrade in the same directory
to retain SmartVPN-data. Android updates retain the application ID and signer.

Windows startup checks the core ABI before enabling Rust workflows and
disables both the host and Rust store on core failure. Android guards the
standalone store and host with successful core loading. The build checks
the Windows service PE architecture and the actual libraries and ABIs in
the final APK. Windows shutdown drains in-flight HTTP responses before
tearing down the Rust host.

Acceptance used isolated portable homes and the read-only Android Studio
API 36 AVD. Both platforms exercised clean install, previous-version data
upgrade, missing core and deliberate rollback. The Android runner checked
the loaded core, host and store, the API worker, persisted node data and TUN
traffic. Windows loaded modules and API reads/writes were checked on the
packaged service. The 0.2.18 Windows archive supplied the old profile since
the archived 0.2.19 service is an ELF executable. Android upgraded the actual
0.2.19 signed APK. Per-path results are under `.tools/phase104`.

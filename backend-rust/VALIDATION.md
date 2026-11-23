# Validation record: phase 1 and Windows opt-in bridge

Date: 2026-11-16. Target: Windows x64, Rust 1.95.0, existing local Go SDK, CGO disabled.

Passed:
- Rust cargo test --offline --locked: 6 tests; 197 Go oracle fixtures; 7 throughput cases; 1004 arbitrary/header-mutated parsing cases.
- cargo fmt --check.
- cargo clippy --offline --locked --all-targets -- -D warnings.
- cargo build --release --offline --locked: real smartvpn_core.dll generated in isolated C-drive staging.
- Default Go build: 59 selected safe tests passed.
- Go -tags rustcore with real DLL: 60 selected safe tests passed, including explicit assertion that Rust DLL loaded and comparison with the Go fallback on all 197 fixtures.
- Go -tags rustcore with absent DLL: explicit unavailable assertion and full fixture/throughput parity passed using fallback.

The 59 safe Go tests come from converter, profile, nodes_uri, speedtest, stun and store test files. Selection is exact top-level test names, not a whole-suite run. SQLite tests use temporary databases. No test uses personal subscription data, starts the VPN service, probes real nodes, or changes network settings.

Not covered: full suite, live VPN, TUN, production service replacement, Android build/device, actual random ID generation in Rust, hostile third-party DLL recovery. Invalid/unloadable libraries and absent symbols fall back; a native library crash cannot be caught by the Go fallback. Use only this locally built trusted library.

Source preservation: existing two Go files receive function-name-only changes, and new wrappers preserve all call sites. Original content is checked against the inspected snapshot before installation. No git commit or push. git status was blocked by the existing working-tree/permission behavior, so no claim of a clean working tree is made.

## Phase 2 validation (2026-11-16)

- 12 Rust test groups passed (6 existing, 5 subscription semantics, 1 subscription ABI contract).
- Offline release DLL build, fmt check and clippy -D warnings passed.
- Default Go path: 59 selected safe tests passed.
- New real DLL: 61 selected safe tests passed, including 10,219 synthetic subscription inputs across all six functions. Valid UTF-8 inputs assert bridge execution rather than silently passing through fallback.
- Old phase-1 DLL: subscription fallback and existing STUN bridge passed.
- Missing DLL: both STUN and subscription Go fallbacks passed.
- Malformed UTF-8 uses Go; Unicode whitespace, mixed-case schemes, all Base64 variants, invalid padding, noncanonical tail bits, ordering, nil-list semantics, capacity retry and synthetic large list tested.
- Every pre-existing file touched by phase 2 is hash-checked against the newly captured current-worktree snapshot before installation. No git commit/push, no actual user profile or node link read by tests.

Known inherited behavior deliberately preserved: multiline YAML input classification
may retain just the `proxies:` line, and `anytls` recognition differs between the
prefix and multiline detectors. These are not silently fixed during a parity port.
No claim of full protocol parsing migration or live network verification.

## Phase 3 validation (2026-11-16)

- Rust: 21 tests passed (previous 12 + 6 protocol field tests + 3 binary ABI tests).
- Format check, strict Clippy, offline release DLL build passed.
- Default Go build: 59 selected safe tests passed.
- New real DLL: 63 selected safe tests passed (previous 61 + node parity/malformed-wire tests).
- 3,593 synthetic protocol cases compare full typed output and exact semantic errors.
- Existing 10,219 subscription-input and 197 STUN corpus comparisons retained.
- Phase-2 DLL: earlier features remain active; missing new symbol falls back for protocol conversion.
- Missing DLL: all pure functions fall back to Go; parity tests passed.
- Independent review found no Windows x64 blocker; changed u64-to-usize length conversion to checked conversion for future 32-bit portability.
- The existing URL parser, endpoint/name handling and runtime lifecycle remain Go. Test endpoints are .invalid or documentation IPs; no socket calls occur.
- Installed source uses snapshot/hash checks; no user configuration, active executable or network settings changed.

No Android/ARM build, full test suite, live VPN, actual nodes or DNS/leak acceptance.

## Phase 4 validation (2026-11-16)

- Rust: 25 tests passed (prior 21 plus 4 extended-protocol tests).
- Offline release DLL, fmt and strict Clippy passed; no external dependencies.
- Default Go: 59 safe selected tests passed.
- New real DLL: 65 safe selected tests passed, including 5,560 additional
  synthetic TUIC/Hysteria-v1 cases and explicit invalid-UTF8 fallback.
- Prior 3,593 protocol-field, 10,219 subscription and 197 STUN samples retained.
- Phase-3 DLL: old three node constructors work, absent v2 falls back for new two.
- Missing DLL: Go fallback tests passed; the DLL-only invalid-UTF8 test is skipped
  in that mode by design.
- Independent implementation review checked version selection and confirmed
  remote query keys cannot override TUIC userinfo adapter fields.
- Existing modified files are checked against a fresh SHA256 snapshot before
  source installation; delivered hashes checked afterwards. Runtime binaries,
  profiles, subscriptions, VPN, DNS, routes and proxy settings are untouched.

Still not full-suite, Android, actual node, speed or leak testing.

## Phase 5 validation (2026-11-16)

- Rust: 34 tests passed; fmt, strict Clippy and offline release build passed.
- Default Go: 71 selected safe tests passed, including 4 new API contract tests,
  7 log tests and the pure takeover decision test.
- Real new DLL: 80 selected tests passed. Missing DLL: same allowlist passed;
  DLL-only boundary tests deliberately skip in fallback mode.
- New VMess/SS/SSR corpus: 3,391 synthetic links with typed-output/error comparison.
  Each protocol also asserts actual v3 bridge execution on a normal input.
- Service/log parity: 5,000 random byte cases, all 16 service decision states,
  output capacity retries, invalid boundaries and legacy Go edge-case fallback.
- Older phase-4 DLL verified independently for v3 and service-export fallback.
- Earlier 3,593 + 5,560 node cases, 10,219 subscription inputs, 197 STUN fixtures retained.
- Independent review identified naive log search worst-case complexity; corrected
  to linear KMP and added 2 MiB repeated-prefix regression before final validation.
- Four API contract tests only register/resolve routes or use in-memory stubs.
  No business connection handlers, real sockets, user profiles or runtime services.
- Full Go application, Android/ARM builds and live VPN/leak/performance acceptance
  remain untested. Go HTTP, storage and platform lifecycle are NOT migrated.

## Phase 6 validation (2026-11-16)

- Rust core: 45 tests, fmt, strict Clippy and offline release DLL build passed.
- Default Go: 90 safe selected tests passed. Real new DLL: 104 selected tests
  passed. Missing-DLL fallback: same allowlist passed (DLL-only tests skip).
- HTTP/selection: exact authorization/identity contract, 2,000 synthetic snapshot
  choices with actual FFI assertions. Canonical routing remains candidate only.
- Traffic: 10,000 value cases, 200 stable sorting sets, explicit 65,536/65,537 row
  boundary and float-overflow fallback. Nine existing safe traffic/history tests.
- Configuration: 512 flag/provider/chain/rule combinations and 12 DNS parameter
  combinations compared byte-for-byte. Capacity retry, invalid UTF-8 fallback,
  ABI malformed input and seven original generated-config tests included.
- Older phase-5 DLL fallback verified per new capability; no loader default changed.
- Rust SQLite candidate: 10 real temporary-DB tests, fmt and strict Clippy passed.
  Includes migration preservation, rollback, concurrent serialization, malformed DB
  protection and invalid-UTF8 TEXT preservation. Go-created -> Rust-updated -> Go
  verified and Rust-created -> Go verified synthetic DB roundtrips passed.
- Android: current core ARM64 release .so linked using existing NDK 27.2/API 26;
  ARMv7 core compile check passed. SQLite candidate ARM64 compile check passed.
  Compiler settings are process-only; nothing installed or globally configured.
- No APK/JNI integration, device execution, actual VPN, live node, leak, network
  performance, real profile, production database cutover or full service test.

Health fail-closed regression: three additional Go decision tests use a sealed
in-memory HTTP transport and injected independent confirmation (no sockets).
They verify bulk failures cannot block a still-working pinned tunnel, truly
exhausted regions select only REJECT rather than a healthy foreign region, and
configured patrol/dwell/retry boundaries remain intact. These tests exercise the
current Go safety policy; they are not a Rust health-scheduler migration or proof
of real HTTPS reachability, long-running timer cadence or end-to-end recovery.


## Phase 7 validation (2026-11-16)

- Reconnection safety: all 150 captured original source hashes matched before
  resuming. No partial Phase7 installation had occurred during the disconnect.
- Rust core: 52 tests, fmt, strict Clippy and release build passed.
- Unified Go allowlist: default 104; new DLL 120; missing-DLL 120 all passed.
- Settings: 1,352 paired-list cases (26 x 26 x 2 platforms), rule/port boundaries;
  exact Windows path normalization remains Go. Existing Phase6 DLL fallback passed.
- Health: 12,000 state/parameter cases with actual FFI assertions. Go retains
  time.Time/monotonic comparisons and every real probe/switch/timer operation.
  Defaults, cooldown/recovery, wrapping counters and the fail-closed regression pass.
- Go storage access is now centralized in store*.go; probe JSON/fingerprint policy
  remains in the application layer. No second database connection was introduced.
- SQLite candidate: 14 tests plus Go cross-language verification. Traffic history
  comparison used 69 snapshots and 324 queries, raw-table equality and writer swaps.
  Both compatibility scripts support the current split Go layout and exit cleanly.
- Android current core ARM64 release link and ARMv7 compile check passed.
- Independent review found no blocking issue. Production Rust storage selection,
  complete HTTP/business handlers, platform lifecycle and live acceptance remain pending.

## Phase 8 validation (2026-11-16)

- Default Go facade and pure-core regression allowlist: 107 tests passed;
  real core DLL and missing core DLL: 123 selected tests each passed.
- Rust pure core remains 52 passing tests with fmt/Clippy/release checks.
- Rust store: 24 tests passed (lifecycle, all operations, raw bytes, partial errors,
  transactions, rollback, persistence), fmt/strict Clippy and release DLL passed.
- Actual Go-to-store-DLL adapter: 10 dedicated temporary-DB tests passed, also
  with both rustcore and ruststore build tags. Every storeBackend method is covered.
- Go/Rust fixture scripts were updated for the facade layout; current and prior
  layouts both passed, including 69 traffic snapshots and 324 queries.
- Single owner per store instance; no Go connection alongside Rust. Missing or
  incomplete DLL can fall back only before any Rust open attempt. No SQL retries.
- Result reads copy cached output rather than rerun a mutation. Capacity limits,
  repeated reads, concurrent close, malformed wire, failing SQL triggers, legacy
  invalid UTF-8/BLOB/NUL and large signed integers were tested.
- Independent review identified claimed/unclaimed result cleanup ownership gaps;
  fixed with retained cleanup-only owners and fault-injection tests before delivery.
- Rare poisoned-library/unknown-close outcomes remain fail-closed, may require
  process exit, and never trigger a second backend. Raw query intermediate memory
  is not globally bounded; input/result limits are explicitly documented.
- Actual runtime remains default Go. No real profile, production database, live
  VPN, DNS, proxy, routes, listener or user node was changed or exercised.

Additional platform compile checks: Android ARM64 default Go/Mihomo backend
built offline as libsmartvpn.so using the repository cmfa,with_gvisor flags.
Both Rust core and Rust store ARM64 release shared libraries also linked with
NDK 27.2/API 26 and explicit 16 KiB max-page alignment. ELF LOAD segments for all
three artifacts are 0x4000-aligned. Artifacts stayed in the isolated build folder;
no JNI wiring, APK installation, device execution or actual VPN test occurred.

## Phase 9 offline validation

- Rust core: 71 unit/integration tests, including 14 workflow tests, one ABI unit
  test and four separate FFI lifecycle/step/limit tests. A 100,000-node workflow
  and 2,000 randomized candidate cases are synthetic only.
- Default Go allowlist: 112 independent tests. Actual core DLL and missing-DLL
  regression suites pass without selecting live networking.
- Full-use-case actual-DLL differential tests: 2,024 Go/Rust comparisons of final
  state, ordered effects/logs, health/event arguments and SaveSettings snapshots.
- In-memory continuation fault tests cover invalid sequences, repeated steps and
  acknowledged-switch completion without replaying network operations.
- Rust store: 24 tests and ten Go storage-DLL integration tests still pass.
- cargo fmt, clippy with warnings denied, and release build pass.

Independent review caught incomplete state after a successful switch followed by
an ABI failure. The corrected driver keeps an acknowledged-switch completion tail
and a Go expected-effect safety mirror. This is an explicit retained Go boundary,
not evidence that all orchestration has moved to Rust. No process-crash recovery,
real network leak test, real user database migration or deployment was performed.

## Phase 10 offline validation

- Rust core: 85 unit/integration tests, including 10 settings-flow tests with
  2,048 privacy combinations and four settings ABI lifecycle/branch tests.
- Default Go allowlist: 118 tests. Existing core/health actual-DLL and absent-DLL
  fallback regressions remain included in test-settings-offline.ps1.
- Settings actual-DLL differential coverage: 2,345 complete use-case comparisons,
  with every fake effect's captured state, parameters/logs, save snapshots and
  final result. Includes partial cache failure and opaque invalid UTF8/NUL values.
- JSON/HTTP tests preserve omitted/null/false/zero/empty and first-error semantics.
- Driver fault injection covers postcommit continuation interruption without
  replaying effects, strict sequencing and the second running-state check.
- fmt, strict clippy and release builds pass. All effects use in-memory substitutes;
  no valid handler was run against user configuration or real network resources.

Phase9 Android ARM64 default Go/Mihomo and Rust core were also recompiled after
its delivery with 16KiB ELF LOAD alignment. This remains compile-only validation,
not Android Rust adapter integration or an installed/connected device test.

## Phase 11 offline validation

- Rust core: 105 unit/integration tests, including 15 registry state-machine tests
  and five independent registry ABI lifecycle/dynamic-branch tests.
- Default Go allowlist: 126 independent tests; all prior actual-DLL/fallback
  regression passes remain in test-registry-offline.ps1.
- Normal supported registry differential cases require a real DLL and zero
  continuation-recovery counter increments, not merely equal final Go results.
  Duplicate-active list inputs are explicit pre-effect Go fallbacks and counted
  separately rather than described as Rust-executed cases.
- Failure/resource tests cover save panic propagation, isolated sealed state,
  no repeated sealing/writes, clone ownership, canceled worker reclamation,
  pre-effect capacity fallback and one-read large-registry continuation.
- Wide Unix timestamp regression passes on Windows amd64 and actual 386 Go tests.
  Values are i64 rather than platform int. A Windows Go race run was not performed
  because a suitable compiler was not available on PATH.
- fmt, strict clippy and release build pass. No real secret, user configuration,
  database, network connection or operating-system network setting was accessed
  by the synthetic effects.

Strict zero-recovery testing identified and fixed two planning mismatches: cached
activation node counts and an unrelated Info field wrongly causing quota-clock
updates during activation. These were not accepted merely because the Go safety
continuation could finish them.

Final registry differential count: 2,520 complete cases = 2,487 actual-DLL cases
with no safety-continuation recovery plus 33 explicitly tested duplicate-active
list fallbacks. The timestamp fixtures include 2147483648, 9007199254740993 and
9223372036854775700; no float64 or platform-int narrowing is used.

Eight registry driver fault/resource tests pass, including 1,200 canceled mirror
workers, a one-read 1,200-entry load continued without repeated deep copies,
sealed-copy isolation, and partial composite panic stopping without further
uncertain effects. No race-detector pass is claimed.

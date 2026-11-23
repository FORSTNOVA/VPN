# Phase 13: experimental quota refresh workflow

This phase covers subscription quota refresh and the shared error-recording
workflow. It does not refresh profile content or alter the running VPN. Default
Go remains selected; the Windows Rust experiment requires rustcore, a core DLL
and the additional SMARTVPN_RUST_QUOTA_FLOW=1 switch.

The state machine spans initial lock/read, unlock and ordered HTTP attempts,
then a fresh lock/read before applying only quota fields. The original first-ID
and exact-URL validation, duplicate-ID behavior, same-ID/URL ABA allowance,
last-completion-wins behavior and non-rollback save failures are compatibility
requirements, not new concurrency guarantees. Old registry snapshots must never
be applied to current application state after the unlocked request interval.

Go retains JSON/HTTP response handling, actual mutex ownership and cleanup,
URL categorization/alternate URLs, the existing proxy selection sub-usecase,
single-attempt GET transport and header parsing, clocks and registry persistence.
The parser intentionally retains existing negative/unknown-key behavior. A
successful response with no quota is not silently promoted to a valid quota.

The optional Rust planner has a suspended Go safety mirror. The executor owns
all real effects; post-effect recovery resumes the same invocation and must not
repeat requests or overwrite intervening edits. Tests use synthetic data, memory
transports and fake persistence. The test-quota-offline.ps1 runner extends prior
regressions; final verification counts and platform limits are recorded below
only after final review. No live endpoint, user database, network setting or
running service is used for acceptance here.

## Offline contract coverage

Independent actual-DLL comparisons passed 1,868 sequential cases plus two
concurrent requests completing in reverse order, with zero recovery/fallback
allowed in those cases. Coverage includes live edits between locks, first-ID
conflicts, deletion and ABA, save failures, retry order, absent-info priority,
full int64 values and unchanged unrelated fields. Fake GET checks the mutex is
available; save/clock/proxy selection require it held; final cleanup checks it
can be acquired again.

The actual Go HTTP adapter is tested over net.Pipe (no OS socket) for GET,
User-Agent, headers-only handling and response closure across
three proxy modes and three response cases. Seven handler rejection cases and
ten header-parsing compatibility cases supplement the complete-flow tests.
These tests do not establish real endpoint, VPN or platform behavior.

Six driver fault/resource tests (three fake and three actual-DLL) cover all ten
effect boundaries with next/advance faults, error-commit/save failures, 64-session
exhaustion/reuse, 1,200 cancelled mirrors and lock/unlock/commit panic cleanup.
Completed GETs and saves are not replayed, committed fields are not rolled back,
and concurrently edited unrelated fields remain intact. Three additional actual
Rust panic comparisons check lock release. The 7/15-second transport settings
were preserved, but elapsed-time timeout behavior was not measured. No Go race
detector or live-network acceptance pass is claimed.

Final staging aggregate passed: 155 Rust tests, fmt/Clippy, 150 selected default-Go
tests, current/prior actual-DLL suites and missing/disabled fallback suites.
Separate storage regression passed 24 Rust tests and 10 temporary-DB DLL tests.
Android ARM64 Go and Rust core compiled successfully; no Android JNI integration,
installation or device/VPN test is implied.

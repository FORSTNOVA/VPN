# Phase 15: experimental kernel lifecycle orchestration

This stage migrates the start/stop control flow behind a default-Go facade and
an optional Windows rustcore planner (SMARTVPN_RUST_KERNEL_FLOW=1). It does not
replace Mihomo or the operating-system adapters, switch a running service, or
change the current connection. kernelExited and its asynchronous identity/lock
ordering remain outside this stage.

Go owns actual runner, relay and configuration objects through invocation-scoped
opaque tokens. Rust sees facts and token identities, never native pointers or
controller secrets. Start and successful publication/time stamping form one
controlled port; allocator results and application port fields are committed
together, including errors. Stop and clearing kernel/active state are similarly
one effect, with config removal ordered afterward.

The compatibility boundary preserves original partial state and cleanup: startup
failure before publication does not Stop or remove config; readiness/owner/nodes/
privacy failures after publication stop once while retaining the original error.
Untransferred local relay cleanup is idempotent; transferred relay belongs to the
app. Existing app relay and new local relay must remain distinct. Nil-kernel stop
still clears app relay/history but does not remove config or clear active state.
The caller's borrowed mutex is never released by this workflow.

All acceptance effects are fake: file reads/writes, port allocation, relay sockets,
runner startup/shutdown, readiness, privacy, history and monitors. No real network,
VPN, system proxy, route or DNS change is permitted for these tests. Prior profile
and config-generation sub-usecases are retained rather than duplicated.

This is offline experimental verification, not runtime/platform acceptance.

## Verified offline checkpoint

- 1,966 actual-DLL full-flow comparisons passed with zero recovery/fallback.
- Five direct Rust nested connection/kernel cases and five dual-selector cases
  passed; Start/Stop counts and both planners' zero-recovery checks are enforced.
- Six safe real-adapter cases cover stub runner publication, caller lock ownership
  and inactive relay ownership. A separate real-executor callback-injection test
  covers lost replies around relay creation/transfer/monitor and idempotent cleanup.
- Actual-DLL fault tests passed 70 next/advance boundaries, eight panic ownership
  cases, 64-session exhaustion/reuse and 1,200 cancellations.
- Final aggregate passed 185 Rust tests, fmt/Clippy, 168 selected default-Go tests
  and all current/prior actual-DLL, disabled and missing-DLL regressions.
- Separate storage regression passed 24 Rust and 10 temporary-DB DLL tests.

A fixture missing the newly required local close callback was corrected and the
entire affected safe/DLL suites plus aggregate were rerun successfully. No
production cleanup rule was weakened to accommodate that fixture. Independent
source/ownership review found no remaining blocker within this scope. No race
detector, live platform, real-time timeout or device acceptance is claimed.
Android ARM64 Go and Rust core builds passed; this is compile-only, not JNI/device integration.

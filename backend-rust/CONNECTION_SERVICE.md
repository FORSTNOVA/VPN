# Phase 14: connection workflow migration boundary

This stage is an experimental planner for connect, disconnect/drop and node
selection. It does not replace the currently running service. Default Go and the
Go safety mirror remain; the Windows rustcore experiment requires an explicit
core DLL and SMARTVPN_RUST_CONNECTION_FLOW=1.

The platform adapters retain Go ownership. Kernel start/stop still include their
existing configuration/readiness/platform internals; listNodes still reads the
kernel/files and assembles metadata. Region locking is decomposed into lookup,
freshness, narrow commit and save.
Patrol stop retains existing nested fail-closed cleanup effects. They must not be
misdescribed as pure getters or harmless timer operations. This stage decomposes
the outer connection decision/compensation workflow, not every platform helper.

Acceptance is entirely offline with fake effect ports. No real kernel startup,
shutdown, group selection, proxy/TUN/DNS/route change, background timer or patrol
is authorized by these tests. The actual Codex connection must stay untouched.

Compatibility requirements include fresh state after the connected-selection
unlock interval, original partial-state/error priority, same-region candidate
selection, chain precedence, raw node names, one-time startup/selection/proxy
compensation and cleanup only of proxy settings owned by this run. A stricter
policy or improved rollback is not substituted silently during the port.

Kernel internals, platform adapters and actual runtime acceptance remain separate
work. The suspended Go oracle remains a safety guard, not a removed backend.

Independent supported-path actual-DLL comparisons passed 2,098 cases with zero
recovery or pre-start fallback: 1,800 randomized, 160 failure-matrix, 100 error
identity, 23 legacy behavior, 6 raw-name/GET and 9 real-mutex adapter concurrent
edit cases. The latter wrap only safe state/lock operations from the real adapter;
all platform operations remain fake. Seven HTTP decoder rejections and one
lock-ownership contract also passed. Borrowed drop never unlocks its caller's
mutex, and connected selection does not overwrite intervening unrelated edits.

Five driver fault/resource tests (three fake and two actual-DLL) passed, including
66 injected next/advance failures across connection, selection, proxy, cleanup
and timer boundaries, 64-session exhaustion/reuse and 1,200 cancelled mirrors.
An unknown-effect panic stops further actions; it does not guess at cleanup or
restart completed effects. Internal drop failures carry a non-nil Err because
the borrowed-lock caller does not use HTTP status. Original error identity is
returned from the Go oracle, not reconstructed from text.

No race-detector, real proxy/TUN, real kernel or Android device pass is claimed.
Android ARM64 Go and Rust core compilation is a build check only.

Final staging aggregate passed: 172 Rust tests, format/Clippy, 161 selected
safe default-Go tests, current and prior actual-DLL regressions plus opt-out and
missing-DLL suites. Separate storage regression passed 24 Rust and 10 temporary-DB
DLL tests. Final independent source review found no remaining blocker within
this offline experimental scope.

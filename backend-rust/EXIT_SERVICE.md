# Phase 16: experimental asynchronous kernel-exit cleanup

The Windows rustcore experiment SMARTVPN_RUST_EXIT_FLOW=1 controls the outer
kernelExited cleanup sequence. Default Go and the safety mirror remain. Runner
objects stay Go-owned; the planner receives only the identity comparison result,
computed after acquiring the app mutex. A stale callback has no cleanup effects.
Original nil/typed-nil semantics are preserved, including repeated nil callbacks
that still match rather than being globally deduplicated.

The workflow retains exit-specific behavior: close relay, stop history without
collecting, clear kernel/active state, remove config, directly cancel patrol,
clear blocking, restore a proxy only if owned, log proxy errors and still release
the platform tunnel. It does not call drop/stopPatrol or add path-monitor cleanup.
A failed ABI continues the same invocation; unknown-effect panic does not replay
cleanup and owned locks are released. The void entry logs an internal error once.

kernel_windows.go remains unchanged: the watcher closes the exited signal before
entering the callback that acquires app.mu. Offline channel tests model this
ordering; they are not live process/watcher acceptance. Platform hooks, relay,
proxy and tunnel operations are all simulated in tests. No running process,
network configuration or user data is touched.

## Verified offline checkpoint

1,770 supported actual-DLL calls passed with zero fallback/recovery: repeated
identity/state combinations, 1,000 randomized cases and two true mutex-wait
identity changes. Three actual-DLL fault/resource tests cover 22 next/advance
boundaries, 11 panic points, 64-session exhaustion/reuse and 1,200 cancellations.
Independent source review and default mutex/identity tests passed; the Windows
watcher source hash matches the previous stage.

Final aggregate passed 193 Rust tests, fmt/Clippy, 174 selected default-Go tests
and all current/prior actual-DLL, disabled and missing-DLL checks. Separate store
regression passed 24 Rust and 10 temporary-DB DLL tests. Android ARM64 Go and Rust
core builds passed only as compile checks. No race detector or live device/process
acceptance is claimed.

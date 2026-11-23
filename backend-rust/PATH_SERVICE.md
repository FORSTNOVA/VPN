# Phase 24: path baseline and recovery

With the rustcore build and a trusted absolute core DLL, SMARTVPN_RUST_PATH_FLOW=1 selects Rust planning for baseline, recovery (all four modes), legacy round and same-region switch. Default Go and continuation-based fallback remain available. Scheduler and control HTTP orchestration remain Go and are not claimed migrated here.

Rust owns aggregate success/failure decisions, target iteration, fixed candidate ordering/filtering, recovery scheduling and switching decisions. Go supplies single-site pathPassed facts, DTO/time parsing, original app state, mutexes, single-site checks, controller GET/PUT, save and events. No actual network or user database is used by the verification fixtures.

Compatibility: baseline always checks Google and YouTube sequentially with the original individual budgets, regardless of enabled flags. Optional targets require the original demand/manual conditions. A path failure does not REJECT or cross regions. Candidates retain exact country, verification and TTL rules; health status alone does not exclude them. An already-current switch returns without revalidation. A different target revalidates region/TTL but not cooldown. PUT precedes pin/save/event; save failure does not roll back. Failed recovery attempts restore the original node while ignoring restore errors.

Original quirks are explicit: recovery captures its candidate pool and region once; cancel does not cancel in-flight work; there is no added generation guard or global running-state recheck. Baseline/legacy clear busy only; recovery also clears Running. Retry remains thirty seconds with equality due. This phase does not fix the scheduler demand double-lock or old-loop identity problem; these are assigned to Phase 25. No Codex-process exemption exists in original path switching, so no real switching is safe to validate in this task.

The mirror advances after each completed effect. Native faults resume that advanced continuation rather than restarting PUT/save/event. Resource and lock ownership stay in Go. Tests use synthetic clocks and fake network/store ports; concurrent-edit cases use real mutexes without sockets. Default, disabled and missing-DLL success scenarios are included.

Initial final verification: 348 Rust tests; 271 explicit safe default Go tests; 2,414 strict actual-DLL comparisons with zero fallback/recovery, including 18 independent goldens. All twenty original messages match UTF-8 source literals. Android verification is compilation only, not device/JNI execution. See phase verification record for aggregate, fault review and source delivery status.

Fault validation: 64 ABI boundaries, twelve post-effect panic cases, opaque endpoint/error boundaries, capacity 64 and 1,200 cancellations passed. Independent read-only review found no blocking issue. Entire nested offline aggregate plus store suite passed in staging, and both Android arm64 artifacts were checked as ELF64/AArch64; these do not establish real network or device behavior.


# Phase 18: region verification job

Scope: start request, asynchronous verification, source selection and result combination, narrow record persistence and counters, final node restore, temporary-kernel cleanup and job completion. Default Go remains available; `SMARTVPN_RUST_REGION_JOB_FLOW=1` plus `rustcore` and the trusted absolute core DLL selects the Rust workflow.

The start response is now copied while holding the app mutex, before spawning the worker. This deliberately fixes the previous unlocked response snapshot race. Other behavior is preserved: initial temporary/original/nodes/delays snapshots; unlock/relock between nodes without new generation tracking; no per-node restore; final restore once unless empty/REJECT; finish(nil) retains an earlier error. A canceled/stopped job still takes the original final cleanup path.

Rust decides source traversal and stops after the first two successful answers, including disagreement. The single-source Go port retains actual proxy HTTP, timeout, response bounds, IP and JSON parsing. Kernel start/stop reuse existing optional migrated sub-usecases; Go still owns the mutex, goroutine scheduling, real transport, persisted data and platform effects. This is not a standalone Rust network engine.

The driver follows a finite trusted Go continuation rather than truncating valid large jobs at an arbitrary total effect count. Per-frame/resource bounds remain. An ABI fault after an effect resumes the same advanced continuation, never starts the whole job again.

Validation is entirely offline with fake scheduling/kernel/PUT/probes/store. See stage verification records for actual counts and final results. Real country verification, VPN selection, OS networking and device behavior are not exercised. No user database is used.

Independent actual-DLL verification: 1,723 full trace/result comparisons plus three start-to-immediate-worker scenarios (six DLL invocations), all zero fallback/recovery. Rust suite: 226 tests. Tests cover source permutations, lifecycle/lock interleaving, TTL boundaries and detached response snapshots. The large-job case also exercises more than 100,000 effects without losing final restore/stop. Full aggregate and post-delivery results are recorded separately.

Fault validation: 46 actual-DLL next/advance boundaries; 22 panic ports; three borrowed-finish panic points; 64-session capacity and 1,200 cancellations. A 12,000-node Go/DLL case completed 132,015 effects with one final restore and one stop, zero recovery. Eight-second probe/delay values are checked as arguments only, not real elapsed-time validation. Internal interruption returns a non-nil error so void wrappers cannot silently report success.
Successful selected lifecycle also runs in default, explicitly disabled, and missing-DLL environments against the full Go effect/state trace. All three passed; production stores and kernel effects are replaced by fakes.

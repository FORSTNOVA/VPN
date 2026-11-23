# Health-service migration boundary

Phase 9 is an opt-in Windows amd64 experiment. Default Go remains the fallback before a use case starts. The health workflow has a separate explicit switch, independent of merely loading the Rust core. Once effects begin, errors stop that attempt rather than replaying it through Go.

Scope: the existing successful-patrol, failed-patrol and unblock entry points. The outer app lock, live kernel checks, active-node query, initial bulk probe, timer scheduling, real HTTPS confirmation and platform network control remain Go. Other callers of the older direct failover/block helpers remain Go.

The Rust continuation consumes a sorted node-index snapshot. Go supplies time predicates so monotonic, timezone, zero-time and duration boundary semantics are not replaced with lossy Unix timestamps. Requests from current production entry points have a nonzero time. Effects carry explicit typed acknowledgments and may be observed once; duplicate or stale step acknowledgments must not repeat an operation.

Compatibility is assessed using final state, ordered effects, effect arguments and the complete SaveSettings snapshot. The effect interface does not expose every intermediate internal scalar, so tests must not claim that broader observation. Synthetic fake effects must never open sockets or use real user data.

Known old behavior retained by this migration:
- Failed bulk requests update only the active node; successful all-zero bulk results update all watched nodes.
- Cached usable latency avoids another group probe. Where there is no usable cache, the old one-extra-probe behavior is preserved.
- Confirmed usable traffic suppresses REJECT for a sweep anomaly.
- A failed target switch proceeds to confirmation and possibly REJECT; a failed REJECT does not commit blocked state.
- Failover saves settings before updating the pinned node. Recovery updates before saving. Persistence errors do not undo successful network operations.
- The separate stop-patrol and region-change paths retain their existing limitations; this experiment is not proof of whole-application leak safety.

Live acceptance, Android integration, process-crash recovery and complete backend replacement are not implied by offline tests.

## Safety mirror and acknowledged-switch completion

The Go driver retains a trusted decision/effect mirror. Every Rust effect must match the expected next operation before execution, including complete watched-node updates, health/dwell/chain gates, candidate choice, exact cooldown transition and completion ordering. This intentionally duplicates some orchestration as a migration safety boundary; it is not removal of all Go business logic.

After an acknowledged successful switch, Go owns a fixed local completion tail. If the Rust continuation then fails, the driver completes only the remaining health/settings/local-state/event steps once. It never repeats a network switch or replays the whole Go use case. Saving old versus new pin retains the original ordering. This guards in-process continuation failures, not process termination or a network request whose remote outcome is unknown.

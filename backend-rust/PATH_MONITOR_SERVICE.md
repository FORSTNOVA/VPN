# Phase 25: path monitor scheduling and control

The independent SMARTVPN_RUST_PATH_MONITOR_FLOW selector (Windows amd64 rustcore with a trusted core DLL) plans start/stop, loop, demand sampling, due retry, normalization, update, snapshot, manual and cancel workflows. Default Go and continuation recovery are retained. Real ticker/channel/select, worker scheduling, app mutex/state, HTTP/JSON and URL/Unicode parsing, platform process facts, controller transport and settings storage remain Go effects. Rust performs workflow and demand/normalization decisions over those facts.

Two intentional fixes differ from the old implementation: the demand branch no longer locks the same non-reentrant mutex twice; old loop identities cannot consume replacement retry state, commit a late traffic result or admit an obsolete queued round. Round identity validation and the Phase24 first app lock are atomic. Once admitted, an in-flight round still completes under Phase24 semantics; stop/cancel do not gain cancellation of an already-started probe.

Preserve baseline interval from settings, 1-second demand/retry ticks, initial asynchronous baseline, synchronous periodic baseline, and asynchronous recovery. Start/stop borrow a caller-held lock. Stop neither waits nor clears busy/results; ticker cleanup belongs only to its invocation and never closes a replacement channel. Partial creation registers ownership before subsequent failure and releases in reverse order.

Demand preserves traffic-query timeout and previous-state behavior: failed GET/no extra targets leave prior samples; successful reads replace samples, ignore smartvpn/empty hosts, detect new or growing-upload connections, match host boundary correctly, sort unique labels and apply the original 30-second throttle. Raw controller endpoint/secret stays in Go. Normalization preserves URL parsing/error order, duplicate URL semantics, 40 Unicode code points and the legacy 14 accepted input positions before preset completion.

Update replaces in-memory settings before save; save failure retains memory and skips restart. Successful update stops/starts before response. Manual requires a running kernel at admission but not Enabled; cancel clears demand/alert/retry only. Snapshot encodes under the lock and derives Running from current busy.

Phase24 integration explicitly preserves a preheld admission lock when native startup/first-frame decoding fails before any effect: default continuation receives the same lock. After an effect starts, normal cleanup remains responsible. Tests must cover both first-frame fallback and first-effect panic, including default/disabled/missing selectors.

Verification is offline with fake event scripts, queues/barriers, network/storage ports and HTTP recorders. No real ticker, listener, VPN, node switch, proxy/TUN/DNS/route change or user database is used. Android is compile-only. Final counts and delivery evidence belong to the phase verification record; implementation is not a standalone Rust service or production cutover.

Independent review found and corrected an initially unbounded Go arena: retry now uses a fixed slot, demand now a stable per-label slot, and terminal save errors a single slot; GET/parse errors are not retained. Target/URL tables intern equal values and each has a 100,000-entry cap, after which the already-advanced Go continuation is used. This is a per-table cap, not a combined total. Long fake retry and failed-GET loops and fixed-slot assertions cover resource bounds.


Final focused verification after the resource fix: 382 Rust tests; 285 explicit safe default Go tests selected by the aggregate; 2,908 strict actual-DLL comparisons, zero recovery per case. Fault coverage includes 48 ABI boundaries, fourteen post-effect panics, three partial ticker creation panics, four guarded-round startup/fault branches, capacity 64 and 1,200 cancellations. Two separate 15,000-tick fake loops validate bounded retry and failed-GET storage. Five original messages remain exact. Full aggregate and source delivery status is recorded separately.


# Phase 17: region locking and report workflow

Default production behavior remains Go. Windows amd64 builds with `rustcore`, an absolute trusted `SMARTVPN_RUST_CORE_DLL`, and `SMARTVPN_RUST_REGION_FLOW=1` opt into Rust planning. A Go mirror validates the complete plan and result; faults resume its already-advanced continuation rather than replaying effects.

Rust owns country validation, lockability, candidate filtering and selection, report name ordering, stale classification and counts. Go retains the actual mutex, independent clock reads, persistence, platform/Mihomo calls, RFC3339 formatting and DTO projection. The existing `blockLocked` health sub-usecase remains a single explicitly mutating Go port with current-state readback. Region verification jobs are not migrated by this phase.

Compatibility includes strict `< TTL` lockability versus `> TTL` report expiration, cooldown exclusions only for candidates, map-key ordering, independent clock reads, partial mutation on save/block errors, log-only pin errors, and clearing a region without unblocking or switching. Returned report sources and job data are detached from mutable app state.

Verification: 1,878 actual-DLL strict comparisons plus one independent complete report golden; zero fallback/recovery required. Fault suite covers 30 next/advance boundaries, 11 panic ports, 64-session capacity and 1,200 cancellation rounds. Seven invalid HTTP inputs and empty JSON shapes are tested without network. Existing exit-model watchdogs were raised from one to five seconds only in tests; production timing is unchanged.

Run `test-region-offline.ps1` with explicit GoExe/GoPath/Cache/RustTargetDirectory parameters. This invokes the earlier safe regression chain, actual-DLL region checks, and fresh-process disabled/missing-DLL fallback checks. All effects are fake or safe in-memory HTTP recording; no real VPN, geo probe, node, route, DNS or user database is touched. Android cross-compilation is not device/JNI validation. Real platform behavior and the retained health block sub-usecase are not newly proven by these region tests.

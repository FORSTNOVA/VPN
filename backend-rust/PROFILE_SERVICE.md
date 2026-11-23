# Phase 12: experimental profile and subscription pipeline

This is an opt-in continuation, not a replacement running service. Flutter,
Mihomo and the default Go backend remain unchanged. The Windows amd64 rustcore
build additionally requires SMARTVPN_RUST_PROFILE_FLOW=1 and an explicit core
DLL path. No migration command changes the installed service or network state.

## Scope

The service facade decomposes cache writes/restoration, selected-node protection,
profile installation and compensation, proxy/URL retry ordering, fetch and refresh,
concurrent subscription acquisition, ordered merge and automatic-merge decisions.
Each batch retains concurrent acquisition and index-ordered processing after join.
The real timer remains Go-owned. Quota refresh is a separate, unmigrated workflow.

## Retained Go boundaries

Go retains actual filesystem calls, HTTP transport, single-attempt status/size
checks, subscription response user-info parsing, URL classification/variants,
YAML parsing/conversion/merge algorithms, configuration facts and rendering,
Mihomo application, settings/node persistence and subscription registry effects.
Rust selects the next operation and applies business transitions; it does not yet
own those adapters. Opaque bodies/nodes stay in a per-invocation Go arena.
The prior registry activation and settings cache calls now enter this facade.

## Safety and failure semantics

A suspended Go oracle validates effects. It does not perform a second copy of IO.
The driver serializes replies before transferring ownership back to the oracle.
After any effect, fallback continues the same invocation rather than restarting
completed downloads or writes. Before-effects incompatibility retains full Go.
Partial file/state failure, errors.Join identity, and failed-live-apply restoration
must remain compatible. Restoration does not repeat the live apply.
Concurrent acquisition workers own separate sessions, arenas and result slots;
the shared input graph must remain read-only until the join.

## Verification

Use test-profile-offline.ps1 in a child PowerShell process. It nests the prior
registry/settings/health/core regressions and runs fake-port actual-DLL comparisons,
temporary-file contracts and opt-out/missing-DLL checks. Store has its separate
backend-rust-store/test-offline.ps1 runner. Never run the unrestricted Go suite.
This document describes the implementation boundary, not live-network acceptance.
Actual downloads, user profile writes, running-kernel configuration updates,
platform integration and Go race-detector coverage are not claimed here.
The experimental path accepts at most 256 subscriptions per parent invocation,
and at most 100,000 manual nodes. Larger snapshots fall back before effects.
Acquisition children encode only their own entry. The ABI has a global bounded
session pool; resource exhaustion uses the same safe continuation, not duplicate
IO. These are opt-in migration bounds, not new default-Go product limits.
Final verified counts are recorded after the final source review.

The safety mirror remains intentionally redundant during this migration. Its
continued presence is a compatibility guard, not evidence of removing the Go
business implementation. Removing it requires a later explicit integration gate.

## Verified offline checkpoint

- 1,994 parent flow comparisons and 8 actual Rust acquisition child comparisons
  passed with zero recoveries for supported normal cases. No fallback was allowed
  to mask these comparisons.
- 12 temporary-directory file-adapter cases passed; fake downloads and fake live
  apply never touch real connections or the user's configuration.
- 9 driver tests comprise 5 fake fault/resource tests and 4 actual-DLL fault tests.
  Actual-DLL cases cover apply success/failure interrupted after the effect,
  512-session exhaustion/release, batch continuation without replay, and child
  session exhaustion after the parent batch has begun.
- Stress includes 1,200 cancelled mirror workers. No Go race-detector pass is
  claimed; this machine has no configured Windows C toolchain for that check.

Important fixes caught by strict differential tests included exact warning/error
wording, nil-versus-empty bodies, empty-ID precedence, quota retention, unrelated
request fields, ordered child effects and a test-fixture deep-copy defect. Test
oracles were repaired without weakening the zero-recovery requirement.

The final staging aggregate test-profile-offline.ps1 passed: 144 Rust tests,
format and Clippy checks, 138 default-Go selected tests, actual-DLL regressions,
opt-out and missing-DLL regressions across all prior workflow stages. The separate
store runner also passed its 24 Rust tests and 10 temporary-DB DLL integration
tests. These counts refer to test cases/functions as indicated, not coverage
percentages or proof of live-platform correctness.

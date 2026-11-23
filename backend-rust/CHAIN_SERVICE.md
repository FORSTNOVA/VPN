# Phase20: chain configuration and pure compilation

Optional `SMARTVPN_RUST_CHAIN_FLOW=1`, `rustcore`, and an absolute trusted DLL select Rust planning. Default Go remains. Six operations cover update, save-profile, prefix, full chain, compile and build. No actual chain probe, dialer measurement or kernel start is performed by this migration test suite.

Rust owns validation order, name/profile conflicts, hop deduplication and unique lookup, strict string-field checks, generated alias/dialer planning, region constraints and commit/rollback decisions. Go keeps YAML/HTTP codecs, file read, mutex and clock, settings persistence, arbitrary Proxy maps, and top-level map materialization. Native data consists of metadata and opaque references, not raw proxies or profile data.

Preserved ordering: update checks running/job even when disabling. Save-profile limits running checks to changes to the currently enabled profile; compile precedes that check and the active case compiles again for build. Profile nil hops counts as absent; empty non-nil hops counts as present. Original is not trimmed. Prefix parses before validating length, while full chain validates length first. Disabled build performs no effects. Region age is read only after status/country match and TTL equality is allowed.

Compilation clones only the top-level map, preserving nested aliases. Invocation references preserve original Chain/Chains/slice identity on failed saves. Pure compiled output is finalized from actual Go maps, not mirror clones. Existing map values with non-string/empty first-hop dialer remain unchanged unless the original rule overwrites them.

Verification uses fake platform/storage/network effects and synthetic profile data. The offline runner includes only explicitly audited tests; real Mihomo/probe tests are excluded. Default/disabled/missing selectors must exercise successful workflows. Android builds are compile-only. See stage records for final verification counts, fault coverage and delivery hashes.

Offline verification: 260 Rust tests; 218 explicit safe default Go tests; 2,005 full actual-DLL comparisons plus four independent legacy identity contracts with zero recovery. Fault suite covers 38 next/advance interruption boundaries, 17 panics including finalization, 64-session capacity and 1,200 cancellations. Nineteen original error literals were compared directly with the pre-migration source. Existing real chain/probe integration tests were not run.

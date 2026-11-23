# Phase23: chain probe workflow

Optional `SMARTVPN_RUST_CHAIN_PROBE_FLOW=1` with `rustcore` and a trusted absolute core DLL selects the probe/list/measure-both/measure-prefix/timed-requests planner. Default Go stays available. Each request measures one supplied prefix; this layer does not invent a parallel batch of prefixes.

Rust controls captured configuration policy, compilation/fingerprint ordering, DNS/ECH decisions, sequential hop construction, cold/warm request scheduling and error classification, observation/report and persistence/list filtering. Go owns original Proxy maps, codecs/hash, actual context/clock/resolver/adapters/dialer/client/body/trace and store resources. Tests replace every dangerous port with a fake or fail-fast guard.

Fingerprint precedes the legacy observable dialer-proxy deletion. ECH clones only its temporary top-level maps. Cold's budget includes adapter setup; warm gets a fresh budget within the same parent context. Body close precedes HTTP204 validation, positive elapsed minimum is1ms, cold failure skips warm and non-reused warm preserves cold with its original error. DeadlineExceeded is distinguished from ordinary cancellation/error. measurePrefix preserves a positive cold result despite warm failure.

Resources are released idle transport first, adapters in reverse order, then resolver reset; body/request/parent contexts are separately owned and canceled once. Fault recovery resumes the advanced Go continuation rather than reissuing requests or saves. Configuration state is captured under the original lock; compile must not introduce unlocked settings reads or erase the captured mode when returning only local facts.

List keeps persisted ordering and legacy Details/Path override rules. Invalid details fails the whole response; invalid path/compilation/fingerprint mismatch skips that record. Measurement precedes the original store-availability check.

All resolver/dialer/HTTP/store/clock operations in workflow tests are fake. A separate adapter contract uses a fake RoundTripper with immediately canceled Go context timers and no socket; it is not a real elapsed-deadline test. Real listener-based and personal chain-probe tests are excluded. Android is compile-only. See phase verification records for final strict-DLL, resource/fault, selector and delivery results.

Offline verification: 317 Rust tests; 258 explicit safe Go tests; 1,844 strict actual-DLL comparisons with zero recovery; 88 ABI failure boundaries, 14 post-effect panic cases, capacity64/cancel1200. Successful default/disabled/missing selectors passed. Twenty-seven original strings match the source. Review fixed an unlocked settings read and ensured a local compile reply cannot overwrite captured TUN/strict-DNS state. Deadline tests use synthetic clocks or immediate context cancellation, not real timeout waiting.

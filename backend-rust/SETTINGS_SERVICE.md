# Experimental settings-update use case

The HTTP JSON decoder and response writer remain Go. The current handler calls a
settings-service facade whose default implementation is the original Go use case.
Windows amd64 builds with rustcore may additionally opt in through
SMARTVPN_RUST_SETTINGS_FLOW=1 and the explicit core DLL.

Rust processes patch presence, validation precedence, privacy override ordering,
the two running-state checks, settings commit, node-state cleanup, cache outcome,
subscription registration and save outcome as a typed continuation. Go retains
opaque settings values, platform/path/URL validation facts, filesystem/secret/DB
adapters and a trusted expected-effect mirror. The Rust module is not a standalone
HTTP service and does not eliminate the Go safety mirror.

clearNodeBoundState is decomposed into its state reset, cached-node removal,
health/region clears and ignored-error settings save. Cache activation remains an
explicit composite Go effect; its possibly partial state is always read back even
when it fails. Settings, cache, subscription and persistence changes are not
rolled back on a later error, matching the prior implementation. Cache lookup may
match ID or URL; subscription registration checks URL only.

After settings commit, an ABI failure completes only the remaining local queue
without replaying file removals, DB clears, cache activation or saves. A final-save
failure still returns 500 and retains prior changes. Before any effect, unavailable
Rust may fall back to Go. After a running-state query or other effect, the whole
use case is never retried through Go.

Offline verification compares the Go oracle with the real DLL using in-memory
fake effects, including every effect's captured state, arguments, logs and result.
No valid production handler is run against user files for these tests. JSON and
HTTP error-contract tests remain read-only. This does not prove power-loss/crash
recovery, Android Rust integration or live VPN safety.

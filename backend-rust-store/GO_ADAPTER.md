# Rust SQLite backend selection

Windows amd64 supports `rustcore` and the standalone `ruststore` build tag.
Android x86_64 and arm64 support `rustcore` with cgo. Ordinary builds use Go.
A configured absolute Rust core path selects the store library in the same
directory (`smartvpn_store.dll` or `libsmartvpn_store.so`). On Windows the
explicit `SMARTVPN_RUST_STORE_DLL` override takes precedence, including an
empty value to disable discovery. `SMARTVPN_STORE_BACKEND=go` selects Go for
the next store open. Each library must supply version 1, all 16 capabilities
and every required export. Windows uses restricted DLL search and Android
loads the named library by absolute path with local visibility.
Missing/incomplete DLL falls back before open only. An attempted Rust open or any
selected backend operation never retries on Go, including uncertain failures.

One facade owns one backend. All operations, result copying and close serialize
under its mutex. The DLL remains loaded. Every call executes SQL exactly once;
result size/read/free copy or release the cached response, not repeat SQL.
The envelope is strictly checked and SQL strings are opaque bytes, with signed
64-bit fields never passing through floating point. Health-parameter JSON remains
in the Go facade. Traffic reports preserve partial decoded data alongside errors.

Open returns a pending native owner: decode success, claim, then free the result.
A decode/open failure frees the pending result to close its unclaimed store.
Failed result cleanup retains a cleanup-only owner in quarantine and in the
returned error. Explicit cleanup may retry only result_free, never open/call.
If cleanup after claim fails, the new backend is explicitly closed; cleanup owners
are retained if release cannot be confirmed. Registry poisoning can prevent all
cleanup and require process exit. This is reported, not disguised as a safe
fallback. A close with unreadable response remains uncertain, not silently marked
closed or replayed on another database implementation.

The ABI caps input at 8 MiB, records at 100,000 and a response at 16 MiB including
its header. These are stricter than unrestricted Go inputs. Limit violations are
explicit failures, with prevalidation before writes and no backend fallback.
Returned-response caps do not yet guarantee a strict bound on all intermediate
SQLite/Rust allocations. This is experimental, not a production-memory-hardening
claim. No actual user database was opened by the verification described below.

`TestRustStore*` uses `SMARTVPN_TEST_STORE_DLL` and temporary independent Go/Rust
databases. It covers all backend methods, raw invalid UTF8 TEXT/BLOB/NUL, i64 beyond
2^53, closed-return conventions, trigger rollback/no retry, overflow partial report,
repeated cached-result reads with a single event insertion, input limits with zero
writes, concurrent close, missing/relative DLL pre-open fallback, and corrupt
open preservation. Cleanup fault tests inject release/close failures and retain
owners. Fixtures are set up and closed before opening Rust; no concurrent alternate
SQLite owner or arbitrary-SQL production opcode is introduced.

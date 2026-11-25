## Phase 8 integration status

The DLL, execute-once ABI and full Go storeBackend adapter are implemented and tested with temporary databases. Compatibility caveats below distinguish standalone convenience APIs from the byte-preserving production-shaped adapter. Go facade JSON/defaulting remains in use even when Rust owns SQL.

# SmartVPN Rust SQLite store candidate

Phase 103 integrates the library on Windows amd64 and Android x86_64/arm64.
Distribution builds select Rust storage as of 0.2.20. Untagged builds use Go. Rust-enabled builds select a compatible
store library beside the configured core. See GO_ADAPTER.md for selection
and rollback and FFI.md for the ABI.

## Implemented

- One mutex-serialized SQLite connection, a 5-second busy timeout, WAL mode, and explicit idempotent close.
- The existing `store.go` schema plus the existing traffic-history schema, copied without column changes. Schema creation and the legacy `probe_results.details` upgrade are transactional and idempotent.
- Probe row read/upsert, structured probe-result save, including Go-compatible path-key HTML/U+2028/U+2029 escaping. Missing/empty hops remain distinct (`null` versus `[]`). Raw details are preserved; **configuration-fingerprint validation and HTTP list filtering remain the caller's responsibility**.
- Settings KV read/upsert; transactional node batch upsert.
- Region read/upsert/clear, health read/upsert/clear.
- Switch event append and recent-history ordering (`at DESC, id DESC`), including the default limit of 50.
- Health parameter save/load with Windows/Android defaults, partial JSON overlay, null handling, case-insensitive known keys, validation, clamping and defaults on error.

Timestamp fields are Unix integer seconds with zero representing unset values, matching the Go storage format; probe `measured` remains Unix milliseconds. Node upsert accepts an explicit timestamp rather than reading a global clock. `load_health_params` returns both usable parameters and an optional error, preserving the existing fallback behavior.

## Boundaries and compatibility limits

- These are storage/data-format ports, **not completed production migration**. The existing Go implementation remains the production owner.
- Traffic history now includes atomic increment/checkpoint writes, retry deduplication, counter resets and negative-counter handling, stale-session checkpoint cleanup, metadata updates, and transactional totals/grouped reads with app filtering, stable ordering and pagination. The original table/index schema is unchanged.
- SQLite and JSON parser error text is not byte-for-byte identical to Go. In particular, health-parameter errors currently do not add Go's `stored health parameters were unreadable:` prefix. A future service/FFI adapter must map errors and validate caller behavior before cutover.
- Rust strings require valid UTF-8; raw arbitrary-byte Go strings are outside this library's typed API. Reading malformed UTF-8 TEXT reports an error rather than silently replacing or discarding bytes; a legacy settings/probe fixture verifies the stored bytes remain unchanged. No actual subscription credentials or user data were used in testing.
- Health-parameter JSON with an unpaired Unicode surrogate in an object key is another known compatibility boundary: Go replaces the surrogate and can ignore the resulting unknown key, whereas serde_json rejects it. This candidate returns defaults plus an error and leaves the stored JSON unchanged. A regression test documents this behavior; production cutover requires resolving or explicitly accepting the difference.
- Region-source comma joining intentionally preserves the existing storage convention rather than inventing a new encoding.
- `Store::open` is a read/write operation and may create files, WAL state or schema changes. Do not point it at a live user profile just to inspect it. Future production rollout needs explicit database-path ownership, backup/rollback, concurrency and crash-recovery validation.
- No concurrent Go/Rust production writers or live handoff have been validated. Each compatibility fixture closes one implementation before opening the other.

## Offline validation

Use the prepared dependency cache, not the user's alternate Cargo registry configuration:

```powershell
# Run from the repository root with a populated offline Cargo cache.
$env:CARGO_INCREMENTAL = '0'
$manifest = '.\backend-rust-store\Cargo.toml'
cargo test --offline --locked --manifest-path $manifest
cargo fmt --manifest-path $manifest --check
cargo clippy --offline --locked --manifest-path $manifest --all-targets -- -D warnings
```

Ten integration tests use newly created, uniquely named temporary databases only: migration/reopen/data preservation, probe upsert/path escaping/order, KV/node transaction rollback, region/health unset timestamps and clearing, switch ordering/limit, health parameter recovery, serialized multithreaded writes, and malformed-database/missing-directory failures, and reporting/preserving invalid UTF-8 TEXT from legacy databases.

The repeatable compatibility check extracts the **actual original Go store/schema and health-parameter code** from the supplied backend directory, compiles an offline oracle using cached dependencies, and exchanges only synthetic databases:

```powershell
python verify_go_compat.py <backend-directory> <absolute-go.exe> <cargo-home>
```

It validates both Go-created DB -> Rust read/write -> Go verify, and Rust-created DB -> Go verify. It checks schema, Go JSON probe details/path spelling, KV, nodes, regions, health, switch events and parameters. Temporary fixture files are removed when the test completes. The Rust example refuses to replace an existing database in create mode.

Verified on Windows x64 with Rust 1.95.0, rusqlite 0.39.0 and its bundled SQLite. Android ARM64 cargo check passed with existing NDK 27.2/API 26 and process-only compiler variables. Android linking, JNI integration, packaging and runtime/device behavior remain unverified.


## Phase7 traffic persistence

`traffic::TrafficSample` accepts an explicitly normalized connection key, metadata and counters. The caller supplies the lowercase SHA256 hex of `session + NUL + ID + NUL + Start`, matching Go, and sets `has_id` from the original ID (empty IDs are skipped). App identity, target and chain formatting remain upstream. `Store::record_traffic` receives the calendar-hour bucket and Unix seconds explicitly; it does not consult a clock, kernel, network or timezone.

`Store::read_traffic_history` preserves report metadata and computes totals, distinct counts and rows in one transaction. HTTP date/group/pagination validation and timezone formatting remain upstream. At the storage boundary, SQLite LIKE, negative LIMIT/OFFSET, and unknown-group behavior match the existing Go method, rather than adding new validation. Arithmetic counter subtraction uses signed wrapping semantics like Go; SQL overflow/error behavior is delegated to SQLite. The existing String/JSON compatibility boundaries above still apply. No production service or live database has been switched to Rust.

Four additional synthetic-database tests cover retry/reset/negative counters, hour/session changes, pagination/filtering/ties, skipped empty IDs, retained network/first_seen versus updated metadata, and trigger-induced rollback of history, checkpoint and stale-session cleanup. Together with the ten existing tests, there are fourteen integration tests.

Run the traffic differential fixture from any directory (it selects C:\ for offline Cargo):

```powershell
python verify_traffic_compat.py <Go-backend-directory> <absolute-go.exe> <cargo-home>
```

It extracts the actual Go storage methods, accepting both the older combined `traffic_history.go` and the newer `store_traffic.go` layout. The oracle retains Go SHA256 key generation; synthetic metadata adapters supply normalized fields. It compares 69 snapshots and 324 queries, exact raw checkpoint/hour rows, bidirectional Go/Rust reads, and retries after switching writers. All databases are uniquely created temporary synthetic fixtures. This verifies the storage boundary, not upstream metadata parsing or calendar/timezone validation.
## Phase8 byte-safe storage and partial reports

The `raw` and `raw_traffic` APIs parallel all storage record operations with `RawText(Vec<u8>)`. Bindings use SQLite TEXT explicitly, including invalid UTF-8 and embedded NUL, and reads preserve TEXT bytes exactly. They never substitute U+FFFD or bind these strings as BLOB. Existing String APIs remain strict UTF-8 convenience APIs. Legacy invalid TEXT is tested directly; legacy BLOB values are read as exact bytes without changing their storage class; unexpected INTEGER/REAL/NULL storage classes are rejected without rewriting, unlike some permissive Go database/sql Scan conversions. This is not a claim of exact compatibility for every damaged or externally modified database.

`read_traffic_history_partial` and its `_raw` counterpart return an outcome containing both the report and optional error. Rows are cleared before beginning; successful aggregate column scans and previously decoded rows remain present on a later failure; has_more changes only after all rows have decoded. Existing Result-returning convenience APIs remain available. The FFI boundary must use the partial outcome rather than discarding it.


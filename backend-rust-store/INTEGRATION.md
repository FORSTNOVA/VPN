# Phase 8 status

The plan below is preserved as the Phase 7 design snapshot. Phase 8 now implements the fixed backend facade, lossless TEXT/BLOB transport, partial reports, versioned DLL/execute-once result handles and experimental Go adapter. See GO_ADAPTER.md, FFI.md and the test runners for current behavior and remaining limitations. Default Go and running application state are unchanged.

# Store backend integration plan (Phase 7 inspection)

Status: design only. The Go boundary is refactored; Rust Store is a tested
standalone library, not a production store backend or a DLL yet. This document
was prepared without opening a user database or modifying implementation.

## One owner per store instance

Introduce `storeBackend` and retain `store` as the facade. Move current SQL
implementations to `goStoreBackend { db *sql.DB }`. `store { backend storeBackend }`
contains exactly one implementation, never both a Go connection and Rust handle.
The facade may keep health-parameter encoding/defaulting above the backend via
setValue/getValue so Go JSON/null/default/error semantics remain unchanged.
Do not select a backend independently for each method.

Suggested interface (existing application types; no SQL objects cross it):

```go
type storeBackend interface {
    Close() error
    upsertNodesAt([]proxyNode, int64) error
    saveRegion(regionRecord) error
    loadRegions() (map[string]regionRecord, error)
    clearRegions() error
    saveHealth(nodeHealth) error
    loadHealth() (map[string]nodeHealth, error)
    clearHealth() error
    appendSwitchEvent(switchEvent) error
    recentSwitchEvents(int) ([]switchEvent, error)
    setValue(string, string) error
    getValue(string) (string, bool, error)
    saveProbeRecord(probeRecord) error
    loadProbeRecords() ([]probeRecord, error)
    recordTraffic(string, kernelTraffic, time.Time) error
    readTrafficHistory(trafficHistoryReport, string) (trafficHistoryReport, error)
}
```

`store.upsertNodes` captures Unix seconds once and delegates to upsertNodesAt;
this gives both implementations exactly the same timestamp. Close on nil facade
remains successful; successful repeated Close remains successful. Other calls
after Close return a closed-store error. Rust close failure currently restores
its connection: retain the handle for an explicit close retry; do not release it
or open Go until close actually succeeds.

Default is Go. Experimental Windows/amd64 build and an explicit startup option
select Rust. Resolve library/version/capabilities BEFORE opening any database.
A missing DLL/export can select Go only at this pre-open step (strict Rust tests
must instead fail). Once Rust open is attempted, any open/migration failure is
returned; do not then open Go. Rust may already have created a file or applied
PRAGMAs even if open ultimately fails. Once open succeeds, every read, write,
transaction and close stays Rust for that instance. No automatic write retry,
including transport/malformed-response errors with uncertain commit outcomes.
Rollback means close all activity/connection, then explicitly reopen in Go at a
controlled restart. No live VPN restart is part of this plan.

## Rust mapping and conversions

- upsertNodesAt -> Store::upsert_nodes(Node[], updated_at).
- regions -> save_region/load_regions/clear_regions. Convert zero Go time to 0,
  other times to Unix seconds; reverse with existing timeFromUnix (0 is unset).
  Preserve sources empty/nil conventions at Go boundary and comma join behavior.
- health -> save_health/load_health/clear_health. Every count and timestamp is
  signed i64; check int narrowing on non-64-bit platforms rather than truncating.
- switch -> append_switch_event/recent_switch_events; nonpositive limit defaults
  to 50; keep descending timestamp/id tie-break order and empty nonnil slice.
- settings -> set_value/get_value; Option None becomes ("",false,nil), present
  empty string becomes ("",true,nil). save/loadHealthParams can remain facade
  functions using these two methods and existing Go encode/decode/normalized.
- probes -> save_probe_row/load_probe_rows (NOT save_probe or ProbeResult JSON
  interpretation). Preserve opaque path/details bytes, nil hops serialization,
  malformed details and application-level fingerprint filtering.
- traffic writes -> record_traffic(session, samples, bucket, now). Go adapter
  computes SHA256(session+NUL+ID+NUL+Start), has_id, trafficApp,
  connectionTarget/Chain, now.Format("2006-01-02T15"), now.Unix(). Supply
  the whole snapshot as ONE transaction. Empty snapshot still clears old sessions.
- traffic reads -> read_traffic_history(report,app_key). Pass full report metadata
  and signed offset/limit unchanged. Preserve [] rather than null rows, app-group
  field clearing, aggregate totals, SQL LIKE, ordering and wrapped has-more math.
  Do not serialize numbers through float64 or JSON generic maps.

## ABI and ownership prerequisite (not implemented)

Add cdylib crate type and a versioned storage ABI, independently versioned from
pure core. Export capabilities covering ALL methods before production selection.
Use fixed-width integers, explicit bool tags, and length-prefixed strings/lists;
no pointers to Go memory retained. Windows path must support Unicode (UTF-16
open API is an option); do not assume ANSI filesystem paths.

Prefer handle registry with generation-checked opaque u64 IDs, not externally
dereferenceable pointers. Keep library loaded while any handle/result exists.
Each Store owns its mutex-protected single SQLite connection. The Go facade
serializes close versus calls; Rust registry lookup retains an owning reference
through each operation. Catch unwind at ABI boundary (no panic across FFI), mark
poisoned handles unusable, and surface error rather than switching backends.

Critical: unlike pure-core helpers, executing an operation again to resize an
output buffer is unsafe. `call(handle, op, input) -> result_handle` executes once
and owns the complete success/error reply. `result_size/read/free` only copies
that cached reply and cannot rerun SQL. Prevalidate all fields before writing.
Writes return only status/error; reads return data and error status. Bound input,
row counts, result memory and live result handles; explicit limit errors never
cause Go fallback on an already selected Rust store. Always free result handles
on Go decode errors too. Never expose a production arbitrary-SQL opcode.

Error envelope: stable category (invalid input, closed, poisoned, SQL, bridge,
unsupported), SQLite extended code where applicable, commit outcome when known,
and safe diagnostic text. Callers must not compare driver-specific error text.
Do not include secrets/SQL parameter values in logs. A successful commit followed
by an output/transport failure is uncertain to caller: report failure, not retry.

## Compatibility gaps to settle BEFORE wiring default/real data

1. Rust API currently returns Rust Strings; Go strings may contain invalid UTF8.
   Define and test lossless SQLite TEXT transport, including read-back of legacy
   invalid UTF8. Rejecting such data is not full behavior parity; replacement
   characters or switching to Go after an attempted operation is unacceptable.
   Embedded NUL and ordinary Unicode already belong in differential fixtures.
2. Rust read_traffic_history currently returns Result<Report> and discards the
   partially updated report on error. Go returns the report with any accumulated
   totals/rows plus error. Preserve that partial payload in the backend response,
   or explicitly narrow/approve the contract after inspecting all callers.
   Check partial map/list results on iterator errors for the same issue.
3. Rust open uses atomic schema/details migration; Go open is sequential. This is
   safer but not identical under injected failures. Test and document rollback
   and database contents, rather than claiming byte-identical failure behavior.
4. Do not use Rust's load_health_params directly initially: keep Go facade's
   defaults/error wrapping until duplicate-key/null/case/number error semantics
   have dedicated end-to-end interface tests.
5. Existing tests directly access db at manual_nodes_test.go:21,
   store_test.go:234/241, traffic_history_test.go:86/96. Replace with test-only
   backend-aware fixture/inspection helpers. Do not keep a second connection or
   add production SQL escape hatch just to satisfy tests. SQL failure triggers
   may be installed in setup connection, closed before opening backend, or via
   a test-only Rust feature that is absent from production exports.

## Offline acceptance suite

- Run every method against independent temporary Go/Rust databases with the same
  operation sequence and fixed timestamps; compare typed replies and canonical
  SQL rows after both handles close. Never concurrently inspect a live store.
- Reopen Go-created legacy databases in Rust and vice versa, including old
  probe schema without details. Compare all six base tables plus traffic tables,
  schema migration and indexes. Existing verify_go_compat.py and
  verify_traffic_compat.py are useful but do not replace the actual ABI tests.
- CRUD, empty results, duplicate/upsert, missing-vs-empty settings, nil/empty
  sources/hops, negative/extreme counters, Unicode/NUL, timestamp0, tie order,
  corrupted rows, malformed JSON, report offsets and limits.
- Atomic node batches and traffic checkpoint+increment: inject trigger failure,
  verify rollback; retry explicitly only in test and verify deduplication.
  Exercise empty snapshots, changed sessions, reused IDs, changed metadata,
  year/hour boundaries and >100 connections pagination.
- Missing DLL/version/opcode: pre-open fallback; selected Rust open failure:
  no Go open. Instrument connection counts and backend dispatch in tests.
- Resize result reads, malformed ABI requests, stale/double-freed handles,
  use-after-close rejection, concurrent calls and close, retained connection on
  failed close. Assert one SQL execution when result is read repeatedly.
- Lock/busy tests using a controlled temporary fixture connection, unwritable
  temp path, read-only/corrupt DB, SQL overflow and invalid UTF8; no network.
- Go default/tagged Windows builds and independent reviewer. Android ABI/build
  remains a separate gate; this Windows loader must not be called portable.

Completion bar: actual application facade dispatches every store operation to
one selected Rust owner, all offline compatibility/failure tests pass, original
Go remains selectable at next clean startup, and no real user DB/VPN was touched.
Until then describe this as prepared integration, not completed store migration.


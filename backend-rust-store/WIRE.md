# Store wire v1

Internal binary ABI, not JSON. All integers are little-endian. `text` is u64 byte length followed by those exact bytes (no UTF-8 validation or substitution). `bool` is exactly u8 0/1. `list<T>` is u64 count followed by records. All other numeric fields are signed i64. Input maximum 8 MiB; list maximum 100,000; result payload maximum 16 MiB minus the 20-byte FFI envelope. Trailing bytes, truncation, invalid booleans/counts and unknown operations are rejected before invoking storage.

FFI result envelope: category u32, SQLite extended code u32, outcome u32, payload byte length u64, payload. Category 0 success, 1 invalid request, 2 SQLite/conversion failure, 3 closed/poisoned, 4 other. Outcome 0 not applied/read failure, 1 success, 2 mutation outcome conservatively unknown. Never retry mutations based on an ambiguous transport result. No SQL strings, paths or raw data are interpolated into error text. Operation 15 can return a nonempty partial report payload on error.

## Record field order

Every field listed below is `text` unless marked otherwise:

- Node: name, protocol, network, tls bool.
- Region: name, exit_ip, country, status, sources list<text>, detail, verified_at i64.
- Health: name, state, consecutive_failures i64, consecutive_successes i64, latency_ms i64, last_success_at i64, cooldown_until i64, updated_at i64.
- Event: at i64, group, from_node, to_node, trigger, evidence.
- Probe: path, fingerprint, delay i64, error, measured i64, details.
- Sample: has_id bool, connection_key, app_key, app, target, network, chain, upload i64, download i64.
- HistoryRow: id, app, app_key, target, network, chain, upload i64, download i64, connections i64, first_seen i64, last_seen i64.
- Report: period, date, group, timezone, upload_total i64, download_total i64, connection_count i64, row_count i64, offset i64, limit i64, has_more bool, rows list<HistoryRow>, sampling_error.

## Operation inputs and outputs

| Op | Input | Success payload |
|---|---|---|
| 0 | empty | empty (liveness) |
| 1 | key text, value text | empty |
| 2 | key text | found bool, then value text only if found |
| 3 | updated_at i64, list<Node> | empty |
| 4 | Region | empty |
| 5 | empty | list<Region>, sorted by raw name bytes |
| 6 | empty | empty, regions cleared |
| 7 | Health | empty |
| 8 | empty | list<Health>, sorted by raw name bytes |
| 9 | empty | empty, health cleared |
| 10 | Event | empty |
| 11 | limit i64 | list<Event> |
| 12 | Probe | empty |
| 13 | empty | list<Probe> |
| 14 | session text, bucket text, now i64, list<Sample> | empty |
| 15 | Report, app_key text | Report, also on storage failure when encodable |

Metadata normalization, SHA256 identity keys, calendar bucket/date validation and timezone labeling remain the Go caller's responsibility. Raw storage preserves SQLite TEXT bytes; legacy BLOB values are also read byte-for-byte without changing their storage class. INTEGER/REAL/NULL values in string columns are rejected, not coerced or overwritten. New writes remain TEXT. No production database selector is enabled by this module.

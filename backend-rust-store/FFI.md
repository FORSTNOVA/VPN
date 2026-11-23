# Store ABI v1

store_ffi.h fixes the transport signatures. version=1, capabilities=0xffff means
all opcodes 0..15 exist, not permission to use a live user database. Every call
executes once and returns an opaque result ID. Size/read copy only that cached
result. Free every result, including errors and decode failures. Never retry a
mutating call on transport/decode failure or switch an already-open store to Go.

UTF16 open supports Windows paths without lossy UTF8 conversion; NUL and empty
paths are rejected. An open result owns the unclaimed store. Claim using both
IDs only after decoding a successful result. Freeing an unclaimed result closes
the store. Close errors retain the handle; stale/reused result IDs are rejected.
Opaque monotonically increasing IDs are never reused; exhaustion fails before
execution. The global registry lock serializes all store lifecycles/calls in this
version. Arc store ownership plus connection mutex prevents close/use races.
No pointer is retained across ABI calls. Unwind is caught; poisoned registry
fails closed for subsequent operations. Arbitrary invalid memory pointers remain
caller UB, as for any C ABI. Rust OOM aborts are not recoverable unwind errors.

Registry limits: 64 stores, 256 cached results, 64 MiB cached bytes. Before any
SQL/open/close attempt, a result slot and worst-case 16 MiB budget must fit. While
SQL runs, the registry lock excludes competitors, so the result slot cannot be
stolen. Input max 8 MiB. Result envelope plus payload max 16 MiB. Wire encoder
checks its size and row-count limits; oversize reads return a cached error, not
a rerun. Critical limitation: typed raw read methods may collect database rows
and strings before encoding. These query/intermediate allocations are NOT fully
bounded by the ABI cache limits. Do not describe the implementation as globally
memory-bounded or ready for hostile/unbounded production databases.

Envelope is 20 little-endian bytes followed by payload: u32 category, u32 SQLite
extended code, u32 outcome, u64 payload length. Categories: 0 success,1 invalid,
2 SQL,3 closed/poisoned,4 other/limit. Outcomes:0 not applied/read error,1 success,
2 uncertain. No SQL argument or secret-bearing driver diagnostic is emitted.
Traffic error responses retain their partial typed report payload. Go must not
throw it away when category is nonzero.

The offline lifecycle integration test uses only in-memory SQLite. It checks
abandoned opens, claim, stale/double-free, repeated reads of a single event insert,
short read buffer, arbitrary TEXT bytes, malformed mutation, result exhaustion
before mutation, store limits and close/call races. Three 6 MiB event rows exercise
oversize result failure then smaller successful query without data modification.
No live app, real user DB, VPN, DNS, routes or sockets are involved.

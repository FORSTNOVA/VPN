# Experimental subscription registry lifecycle

The service facade covers load/save/ensure, ID and URL lookup, listing, add/update/
rename, usage/error records, auto-merge hours, delete and activate. Default Go is
retained. Windows rustcore builds require SMARTVPN_RUST_SUBSCRIPTION_FLOW=1 in
addition to the explicit core DLL to select the experimental continuation.

Rust plans registry lifecycle decisions and metadata transitions. The Go oracle
runs as a suspended safety mirror, never as a second IO executor. A synchronized
channel yields each expected effect; the driver validates the Rust request,
executes that effect once and returns its result and actual partial state. If a
continuation fails after effects, only the remaining operations of that same
oracle invocation may complete. This is a retained Go business/safety boundary,
not a standalone Rust backend or removal of Go registry logic.

Persistence uses an isolated sealed copy. That copy must never replace the live
plaintext registry. All seals must succeed before a write; no plaintext fallback.
Composite fetch/cache/merge effects always return their actual state even on
failure. JSON decoding/encoding, URL/hash/label compatibility, actual secret and
filesystem/SQLite ports, timer execution and download/cache/merge internals remain
Go. Refresh/quota and automatic-merge scheduling are still separate unported
use cases. These retained boundaries are not counted as migrated internals.

Supported-path differential tests must require both a real DLL and no increase
in the Go-continuation recovery counter. Known unsupported inputs, including
multiple active-URL list entries with the original non-strict Go sort behavior,
are tested as explicit pre-effect fallbacks and counted separately. Bounded
experimental capacity and recovery are not performance guarantees.

No tests may use actual secrets, subscriptions, user database/configuration,
network sockets, live nodes or OS network changes. Compilation is not deployment.

## Experimental bounds and concurrency

The Rust-selected path is currently bounded to 256 registry entries and a combined
4,096 health/region/cached-node records in the input snapshot. Larger starting
snapshots fall back before effects. A larger decoded registry discovered after the
read continues the same suspended Go invocation without rereading the database;
its remaining state graphs use synchronized single-owner transfer rather than
quadratic repeated deep copies. Such continuations are counted separately from
successful all-Rust planning.

The safety mirror owns one goroutine, blocks at each effect and is canceled/joined
on every exit. Normal bounded steps deep-copy mutable state. Isolated sealed
storage snapshots and live state are distinct. Time values use explicit i64 across
32/64-bit boundaries. Resource/ownership tests do not substitute for a race-detector
run; a suitable Windows race-build compiler was not available on PATH.

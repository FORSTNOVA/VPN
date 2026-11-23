# Rust host and loopback monitoring API

The Rust distribution starts the HTTP API through the platform service.
The standalone executable also supports monitoring and offline replay.

## HTTP monitor

### Windows client integration

The Windows Rust distribution includes smartvpn-host.exe next to the service.
The service discovers the host on startup. SMARTVPN_MANAGED_HOST can name an
absolute replacement executable. SMARTVPN_BACKEND=go or the Go rollback
package selects the Go service paths on restart.

In managed mode Rust starts Mihomo with the configured profile, appends its
output to mihomo.log, and reports the kernel PID and a private monitor address
to Go. Windows places both processes in a kill-on-close job. Disconnect or
loss of the parent pipe stops the kernel; unexpected kernel exit ends the
supervisor and triggers cleanup for the matching runner. The Windows adapter
verifies port ownership and performs OS effects. Authenticated
traffic and latency requests use the current runner's private monitor. The
node timeout comes from current app settings on each list-latency request.
In-flight requests are canceled when that runner exits. The Android runner
continues to use its in-process kernel and VpnService.

With a Phase 101 core and supervisor, the second startup line includes a
`v2 ` prefix. That generation accepts `PUT /managed/config` with a separate
write token and forwards it to its authenticated Mihomo controller. An older
pair uses the direct controller path. The Windows proxy helper can invoke
`--proxy-restore-plan` to select owned registry values before restoring them.

### Integrated service API

Phase 100 uses one service-lifetime Rust listener for the public API's GET,
HEAD, POST, PUT and DELETE methods. Windows launches the sibling executable
with `--api`. Android opens `smartvpn_host_api_start_v1` in the selected host
library. The public address and client token stay the same. Identity remains
public. All writes require the service token.

Rust matches the method and path, authenticates the request, receives the
complete write body, and calls one numeric operation on a private loopback
adapter. The adapter has a separate credential. It invokes the existing
transaction or platform handler with the original query string and the same
64 KiB JSON input window as the Go decoder. The service streams the reply.
An interrupted upload cannot invoke a write. If an acknowledgement is lost
after invocation the request returns an error and is never replayed.

The core capability `smartvpn_write_api_version_v1` and successful host
startup select full dispatch. An older read-capable pair uses `--reads` or
`smartvpn_host_reads_start_v1` and local Go writes. An unavailable host uses
the Go router. The standalone controller monitor below remains read-only.

### Standalone launch

Run `smartvpn-host --http` for idle monitoring or
`smartvpn-host --http <controller-port>` to query an independently managed
Mihomo process on 127.0.0.1. The parent supplies a private, per-run
`SMARTVPN_MONITOR_TOKEN` in the child environment and, when attaching,
`SMARTVPN_MONITOR_CONTROLLER_SECRET`. The first stdout line gives the
ephemeral loopback listen address. Use the monitor token in the Authorization
Bearer header. Keep both credentials out of command-line arguments and logs.
The parent owns process termination and must relaunch the monitor if the
controller port or credentials change. No system proxy or TUN setup is involved.

GET and HEAD support the following canonical paths:

| Path | Source |
|---|---|
| `/api/identify` | Public; connected reflects an authenticated controller version response. |
| `/api/latency` | SmartVPN group delay test, current selection and optional auto/fallback group resolution. |
| `/api/nodes/latency` | SmartVPN group delays with the configured node timeout. |
| `/api/traffic` | Connection totals, per-second rates and up to 80 ranked connection rows. |

Latency queries ask Mihomo to measure the standard YouTube target; traffic
queries only read its connection table. For node tests the parent can supply
`SMARTVPN_MONITOR_NODE_TIMEOUT_MS` (1000–15000). Defaults are 3000 on Windows
and 6000 on Android. Attachments are read-only; controller failures return
the corresponding query error and a failed traffic sample clears the rate
baseline. Stop the host to release its listener. It does not start or stop
the attached kernel. Other registered business routes return 501 after auth.

## Ownership and execution

`Host<R>` exclusively owns one `service_state::State` and one `Resources` adapter.
It is not `Clone`; request execution requires `&mut Host`. Admission creates up
to 64 opaque tickets. Shutdown first closes admission/listener, then waits for
already admitted tickets to be executed before closing store/state. The adapter
is borrowed only during an individual call, never copied into worker graphs.
Errors closing resources are retained; calls are never automatically repeated.
The caller must explicitly drain admitted work and call `begin_shutdown`.

Request IDs are nonzero and strictly increasing at admission. The latest receipt
binds the full request payload to its result (including errors). Exact replay
returns that result without executing effects; a changed payload conflicts.
Older IDs are rejected, not replayed. This is a bounded one-receipt protocol,
not an unbounded response cache. Admission is serialized; execution of queued
mutations must use their expected revision and may fail if reordered.

The synthetic `SetSamplingError` request uses the existing core epoch/revision,
command validation, pending and acknowledgement protocol. State commit precedes
`Resources::persist`. A persistence error or panic is uncertain: the state is
not rolled back and new work fails closed. There is no automatic store retry or
Go takeover. The exported pending checkpoint can be inspected, but importing or
recovering it into another memory host is not implemented; the separate disk mode below provides strictly validated offline recovery. `MemoryResources` retains a
pending checkpoint for demonstration; it provides **no disk durability** and
all state is lost when the executable exits. Production checkpoint persistence,
crash recovery and resource-identity reconciliation remain required adapters.

HTTP-shaped requests reuse `http_dispatch_flow::plan/execute`, which delegates
route/auth decisions to `http_core`. This kernel-free host serves identity and
the idle responses for `/api/traffic`, `/api/latency` and `/api/nodes/latency`.
GET includes the response body and HEAD returns the same status with an empty
body. Other authorized business routes return 501. The transport uses binary
frames over pipes. Network monitoring is available through the separate
`--http` mode above.

## Framing

Outer frame: little-endian `u32` payload length, at most 1 MiB. Payload starts
with `id:u64, op:u8`. Byte strings use `u32 length + raw bytes`.

- 1: method, canonical path, authorization byte strings.
- 2: expected revision (`u64`) and synthetic sampling-error byte string.
- 3: state metadata snapshot.
- 4: explicit shutdown (no further fields).

Response frame: `status:u16, revision:u64, body:bytes` using the same outer frame.
The only authorization token is the synthetic value `offline-synthetic`; no
credentials are read from the system. Typed commands are a local test protocol,
not remotely exposed HTTP mutation endpoints. Truncation/oversize/unknown op
stops the stream without resynchronization or retry. Output failure stops the
stream after any already committed effect; it never retries the request.
EOF triggers shutdown in the framed modes.

Fetch the host Cargo.lock dependencies once with `cargo fetch --locked
--manifest-path backend-rust/host/Cargo.toml`, then run `cargo test --offline
--locked --manifest-path backend-rust/host/Cargo.toml`. Hyper and Tokio supply
HTTP/1 transport. Set SMARTVPN_TEST_OFFLINE_HOST to the built executable to
run the Go tests TestOfflineHostIdleParity and TestRustHTTPMonitor. The latter
owns temporary loopback listeners and a synthetic Mihomo controller.


## Phase43 dedicated-directory disk mode

`smartvpn-host --offline-disk <directory>` accepts the same synthetic framed
requests. The caller must create and own a dedicated directory. Tests create
unique TempDir directories; this is never pointed at the application's settings
or user database. One synchronous request is admitted at a time in this mode;
the original MemoryResources host retains its 64-ticket API unchanged.

The binary DurableEnvelope contains the complete validated core checkpoint,
sequence, request high-water mark, full unresolved intent, and one full response
receipt. Each mutation publishes intent before applying state, then publishes
the pending checkpoint, acknowledges in memory, publishes the final checkpoint
and receipt, and only then replies. Reads also publish intent and a receipt.
Exact latest-request replay returns the stored response without any publication;
older IDs and same-ID changed payloads are rejected. A publication error/panic
poisons the current host, with no automatic retry or rollback. Reopening an
intent or pending checkpoint returns RecoveryRequired without reapplying or
acknowledging anything. A final file that was committed before a lost response
can legitimately restore and replay its final success receipt.

Core `State::from_checkpoint` first validates the entire checkpoint and retains
last command, ack, epoch and pending. Disk host recovery additionally rejects
non-default path/health/control/catalog/China domains and any baseline/clock
resource token. This conservative first version restores only the offline
sampling-error state and its history: it does not manufacture resource tables,
clock ledgers, active leases, Info handles, listeners or platform objects.

Publication uses a versioned, at-most-128MiB binary codec with strict lengths,
booleans, trailing-byte rejection and an FNV-1a corruption checksum. This checksum
is NOT authentication; the directory and files must be trusted and exclusively
owned. Snapshot and mutation receipts are validated against core authority;
HTTP-shaped receipts are recomputed with the supplied token/platform facts.
An SVHOST01 HTTP receipt replays the original identity-only dispatch behavior.
New HTTP receipts use SVHOST02, so restart validates the original response
under the rules that produced it. An older host rejects a latest SVHOST02
checkpoint. Use a separate dedicated directory for testing older binaries.
No real credentials should be supplied to this synthetic offline test protocol.

An exclusive create-new owner.lock prevents another cooperative writer. A
crashed process leaves the lock; no stale lock is automatically stolen or
removed. Clean close/drop releases only the held ownership. Recovery of an
orphan lock requires an explicit operator decision outside this implementation.

Each publication writes a new sequence .tmp with create_new, syncs the file,
and renames it within the directory to a never-reused .checkpoint name. Existing
final files are never intentionally overwritten. Unix additionally syncs the
directory. Windows std has no portable directory fsync here: Windows guarantees
are limited to observed process-restart publication tests, NOT sudden-power-loss
durability. The journal selects the highest formal sequence; invalid/corrupt
latest files fail closed rather than silently falling back to an old checkpoint.
Unpublished temp files are not commits and are never silently overwritten.

The journal is append-only with no compaction or retention policy. Disk usage
grows by two publications per read and three per mutation; this is a bounded
per-record codec, NOT bounded lifetime disk usage or a production storage layer.
Production storage retention, encryption/authentication, hardened directory/ACL
management, hostile concurrent filesystem changes, durable resource restoration,
HTTP transport and real application handler adapters remain out of scope.

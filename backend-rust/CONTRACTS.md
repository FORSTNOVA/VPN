# Local API compatibility contract (phase 5 baseline)

This inventory is extracted from the current Go implementation. It is a baseline,
not a claim that HTTP service ownership has migrated to Rust. No actual service
or node was started to produce it.

## Transport and authorization

`main.go:serveLocalAPI` binds IPv4 `127.0.0.1:0`, avoids the configured SOCKS port,
uses a 3-second ReadHeaderTimeout and publishes the chosen API port. The Flutter
bootstrap DTO contains `port`, `token`, and optional `error`.

`main.go:authorize` requires an exact case-sensitive header value `Bearer ` plus
the token. It returns 401, `text/plain; charset=utf-8`, body `unauthorized\n` on
failure; authorized handlers start with `application/json; charset=utf-8`.
Only identity is public. HTTP method matching is Go ServeMux behavior: GET also
matches HEAD. Handler business errors may override the initial content type.
Identity returns only `app` (SmartVPN) and `connected`; do not expose tokens,
subscription URLs or runtime configuration on this public endpoint.

## Registered common routes

Each entry is an exact Go ServeMux pattern. All except identity use authorize.

- `GET /api/identify`
- `GET /api/state`
- `GET /api/subscription/policy`
- `PUT /api/settings`
- `POST /api/connect`
- `POST /api/disconnect`
- `POST /api/proxy/reapply`
- `GET /api/nodes`
- `GET /api/nodes/latency`
- `POST /api/nodes/fetch`
- `GET /api/diagnostics/subscription`
- `POST /api/nodes/refresh`
- `PUT /api/nodes/select`
- `POST /api/shutdown`
- `POST /api/direct/refresh`
- `GET /api/latency`
- `POST /api/diagnose`
- `POST /api/speedtest`
- `GET /api/path-checks`
- `PUT /api/path-checks`
- `POST /api/path-checks/run`
- `POST /api/path-checks/recovery/cancel`
- `GET /api/traffic`
- `GET /api/traffic/history`
- `GET /api/nodes/manual`
- `PUT /api/nodes/chain/profile`
- `GET /api/nodes/probes`
- `POST /api/nodes/chain/probe`
- `PUT /api/nodes/chain`
- `POST /api/nodes/manual`
- `DELETE /api/nodes/manual`
- `GET /api/subscriptions`
- `POST /api/subscriptions/activate`
- `POST /api/subscriptions/add`
- `POST /api/subscriptions/update`
- `POST /api/subscriptions/merge`
- `POST /api/subscriptions/refresh`
- `POST /api/subscriptions/quota`
- `POST /api/subscriptions/auto-merge`
- `PUT /api/subscriptions`
- `DELETE /api/subscriptions`
- `GET /api/regions`
- `POST /api/regions/verify`
- `POST /api/region/lock`
- `GET /api/events`
- `PUT /api/health/params`
- `POST /api/health/patrol`
- `GET /api/tun`
- `POST /api/tun/check`

Windows additionally registers `POST /api/tun/wintun`; Android adds no routes
(`main_windows.go:registerPlatformRoutes`, `main_android.go:registerPlatformRoutes`).

## JSON wire semantics

`main.go:decodeJSON` reads at most 64 KiB and rejects unknown struct fields.
It decodes one JSON value only, currently accepts trailing JSON values, permits
`null` for a struct destination and uses last-wins duplicate fields. Tightening
these is an API behavior change, not a parity port. `writeJSON` emits a newline.

`proxyNode` always emits name, type, tls, wsHostConfigured, wsPathConfigured,
alive, selected; sourceLabel/network/manual are omitted when empty/false.
`cachedNodeList.nodes` distinguishes null from an empty array. Preserve this
when replacing Go slices with Rust Vec/Option. Times elsewhere use Go time.Time
JSON strings, not epoch integers; persistence schemas also contain integer times.

`state` is not just serialization: it locks app state and can consult running
Mihomo/proxy/TUN/health state. Its response includes settings, resolved paths,
ports, connection status, selected/active nodes, health and region status. A Rust
implementation needs injected platform and kernel interfaces before safely
contract-testing this handler. Do not run it against the user's live service.

## State and persistence boundaries

- `app.mu` serializes mutable settings, kernel/control state and health jobs.
  Background patrol, region verification, path checks and auto-merge have their
  own cancellation lifetimes. A Rust rewrite must define lock/cancellation order
  rather than merely translate fields into independent mutexes.
- `main.go:loadSettings/saveSettings`: settings.json; platform seal/openSecret
  wraps the subscription URL. Loading migrates legacy strictPrivacy and chain
  fields and fills defaults. Saving updates log-redactor secrets. Do not silently
  replace platform secret formats with plaintext or make load trigger writes.
- `nodes-cache.json` ties cached nodes to subscriptionHash; generated runtime
  config is `config-<pid>.yaml`; stale config cleanup is side-effectful.
- `profile.go`: providers/subscription.yaml; `manual_nodes.go`:
  providers/manual.yaml; `subscriptions.go`: subscriptions/<id>.yaml.
- `store.go`: SQLite nodes, health, region_verify, switch_events, settings_kv,
  probe_results. One DB connection serializes writes, busy_timeout is 5000 ms,
  journal mode WAL; schema upgrade adds probe_results.details when missing.
- `traffic_history.go`: traffic_checkpoints and traffic_hours are committed in
  one transaction to prevent retry double-counting. Preserve reset and session
  semantics; migrations must test rollback with a temporary DB.
- `servicerecord.go`: service.json contains PID, port, token and optional binary
  size/mtime; writes through .tmp then rename; removal checks PID ownership.
  Rust must not remove or replace another running service's ownership record.

## Remaining acceptance risks

1. Use isolated fixtures for every settings/profile/schema migration, including
   Windows and Android secret handling and failed writes. No real user data.
2. Preserve auth before any effect; preserve status codes, error body format,
   methods, null/empty fields and numeric/time representations. Route inventory
   is not full request/response schema coverage for all business handlers.
3. Use fake Mihomo/platform adapters for connect, disconnect, proxy, TUN, shutdown,
   fetch, diagnostics and monitoring. Tests must not call live business handlers.
4. Verify Android JNI and Windows process/service ownership separately; a working
   Windows DLL does not prove a portable standalone Rust service.
5. Only controlled isolated-machine acceptance can validate actual tunnel lifecycle,
   DNS/leak protection and crash recovery. None are established by these tests.

## Added offline regression tests

`migration_contract_test.go` pins route lookup (never invokes route handlers),
authorization with a harmless stub, JSON decoding edge semantics, DTO null/empty
shape, and public identity on an inert app. Uses httptest recorders/requests only,
not httptest servers or listeners. This is a partial contract foundation.

## Phase 6 implemented HTTP core

`src/http_core.rs` implements byte-exact authorization, fixed identity JSON,
canonical method/path dispatch, and transport-independent request planning.
RequestContext supplies a current connected-state snapshot, not a competing
copy of kernel state. ResponsePlan explicitly delegates business routes; it
does not pretend connect/settings/persistence actions took effect.

Go authorize and identify now call the opt-in Rust bridge. Default builds,
old DLLs without all HTTP v1 symbols, oversized auth input, and invalid ABI
results use the original Go path. New ABI is documented in http_core.h.
SMARTVPN_TEST_EXPECT_HTTP_RUST=1 requires real bridge availability in the
Windows tagged parity test, including response-byte and content-type checks.

The planner is an offline service core, not the live router. Go ServeMux still
handles path cleaning, redirects, escaped segments, business handlers, listener
and lifecycle. Stateful Rust service replacement and full response schemas
remain incomplete. No live network acceptance is claimed.

The application reachable-node selection is also actively bridged:
`main.go:chooseReachableNode` retains its original Go oracle/fallback and passes
only signed delays and a preferred-node index to Rust. Rust preserves positive
reachability, preferred-node priority, minimum-delay choice and stable ties.
Names, probing and connection actions remain Go-owned. A 2,000-case synthetic
comparison exercises duplicate names, absent preferences, nonpositive delays,
empty lists and ties, and requires actual Rust use with the new DLL.

Active opt-in Rust calls: authorization predicate, identity JSON bytes,
reachable-node selection. Offline/candidate only: route table and request planner.
No live listener, app-state lock orchestration, or business mutation moved yet.

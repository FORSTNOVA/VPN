# Phase21: traffic and history workflow

The optional traffic workflow covers current traffic, history start/stop/collect, the event-driven collection loop and history query. Default Go remains; Windows amd64 `rustcore` with `SMARTVPN_RUST_TRAFFIC_FLOW=1` and a trusted absolute core DLL selects Rust planning. Existing rates, ordering and SQLite backends are reused rather than reimplemented.

Rust owns orchestration and branch decisions. Go retains actual channel/ticker/select, mutex, HTTP snapshot acquisition, date and pagination parsing, row conversion, clock and DB transactions. Native references are opaque. The ticker is not simulated in production: the existing one-second timing and Go select event choice remain at the Go boundary.

Start publishes cancellation before spawning, without immediate collection. Each tick checks the current channel identity under the app lock; an old loop never clears a newer loop. Stop only closes and clears the current cancellation handle, without waiting or sampling. Existing normal kernel stop still does its separate final collect before stopping history; asynchronous exit does not add a sample.

Current stopped state preserves its baseline; GET failure clears it. History skipped collection preserves previous errors; successful record clears them. History queries preserve validation order and independent date/timezone clocks. No added trimming or cancellation priority is introduced.

Tests use fake wait/tick/cancel selection, kernel GET, persistence and clocks, with actual in-memory mutex/channel state where necessary. No real background-collection test, socket, user database or actual VPN is used. Scripted zone changes verify clock separation rather than real-world elapsed DST behavior. Require successful default/disabled/missing selector cases, strict DLL comparisons, no-replay/resource tests and independent review before delivery. Android compilation is not runtime opt-in validation.

Independent offline verification: 279 Rust tests, 230 default Go safe tests, and 1,902 actual-DLL strict calls (matrix/random/golden/shared lifecycle) with zero recovery. Successful default/disabled/missing selectors and safe HTTP paths passed. Review fixed a time.Time/private-field comparison mismatch and registered ticker cleanup before creation for partial-creation panics. The existing kernel stop/exit composite call order is unchanged and is exercised by inherited fake regressions, not a new real kernel test.
Fault/resource checks: 42 ABI boundaries, 12 completed-port panic points plus one ticker-creation partial-allocation panic, 64-session capacity/1,200 cancels, and 15,000 scripted ticks (~120,000 effects), no fallback or repeated effects. These are simulated event sequences, not 15,000 real seconds.

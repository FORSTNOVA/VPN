# Phase41 traffic/metadata/config compatibility closure

## Selected production boundaries

- `traffic_projection`: rows/process/target/rule/chain/app identity/date plan/date validation/pagination. HTTP current/history, Go/Rust stores and path demand share the original public helper entrypoints. Native owns priorities, positive-duration gating, stable signed-wrapping traffic rank and first-80 truncation, UID/path identity, period/layout choice, roundtrip acceptance and pagination bounds. Go supplies byte-exact standard Unicode/path/host/time/integer facts; no actual measurements are performed by this module.
- `metadata_projection`: manual details (last matching name wins), domains (original order and duplicates), proxy grouping (subscription then manual, all same-name maps appended), current-index (subscription setting, conditional read, original ignored read error plus returned bytes, YAML decode, manual graph), and untrimmed platform-mode selection. Go holds exact original maps, errors and resource identities. Native traversal emits token-addressed effects. Names/modes are byte-preserving facts, not an implicit UTF-8 validity filter.
- Config v2 receives raw quoted rules plus standard split/trim facts, selects the original fixed third field and DIRECT/REJECT/proxied buckets, preserves order and duplicate rules, and generates Windows/Android TUN templates from platform/descriptor facts. It does not parse a more sophisticated rule grammar or access a live descriptor. V1 exports remain; the production v2 loader checks version/export before constructing input and falls back for old libraries.

## Runtime and recovery

Default Go remains available. Traffic projection is pure and checked against its trusted Go oracle; invalid/unavailable native results use the pure result without I/O replay. Metadata uses mirrored continuation and exact original graph custody; failed native steps cannot repeat already applied append/update/read effects. These are optional Windows `rustcore` capabilities, not Android runtime integration or a new standalone host.

Traffic ABI has a 64 MiB frame/output budget and 100,000 item limits; exceeding these invokes pure Go compatibility, not dropping user records. Standard parsers and data ownership are deliberately retained. Native path/date facts preserve Go Unicode behavior and local-time identity.

## Acceptance and exclusions

Run `test-projection-config-offline.ps1` in a fresh process. It wraps all previous explicit-safe aggregates plus native, disabled and missing-library regression. Independent old-source hard-coded goldens supplement strict direct-native comparisons; a recovered result is not counted as native success. Config old-v1 DLL compatibility is a separate explicit fixture test and does not make the aggregate depend on a private stage path.

Tests use synthetic graph/codec facts, fake reads/effects and TempDir. No running VPN/Mihomo, node, DNS, socket probe, driver, route, proxy, registry, real descriptor, production database or deployed binary is touched. Android outputs are compile-only. Whole backend replacement, production default selection, deployable signed packages, live upgrade/soak/performance and real-device acceptance remain separate uncompleted work.
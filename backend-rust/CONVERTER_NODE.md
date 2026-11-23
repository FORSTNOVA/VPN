# Subscription conversion and node frontend closure (Phase 39)

Default Go and per-capability fallback remain. The Windows amd64 rustcore path may
select SMARTVPN_RUST_CONVERTER_FLOW and SMARTVPN_RUST_NODE_FRONT with the chosen
DLL. These are compatibility planners, not a standalone Rust HTTP server or a
production activation. Tests never consume real subscription credentials or URLs.

The converter controls Sing-Box protocol/default/TLS/transport field plans, raw
and decoded format priority, filtering and projection, YAML byte-preservation,
source merge/naming and group/rule rewrites. The node frontend controls URI
front-end dispatch/finish, manual VMess preparation, transport option plans,
profile extraction/first-name dedup, subscription URL policy and common matching.
Existing native protocol builders are explicit children, not repeated parser
implementations. Standard JSON/YAML/URL/base64 codecs remain Go.

Original byte slices, decoded maps, nested graph aliases, node backing arrays and
credential values remain invocation-local Go resources. Native plans refer to
resource tokens and scalar facts. Ordered mutations execute once; malformed ABI
or native failure continues the trusted paused invocation rather than restarting
conversion over an already-mutated graph. Parent workflows call these new entry
points without opaque whole-conversion or whole-merge effects.

Do not normalize the original dialects: subscription URI VMess, manual VMess and
Sing-Box VMess intentionally differ in defaults, validation, fragments, TLS and
transport-error handling. Proxy/node lengths can differ; merge renames nodes by
index as before. Duplicate old-name aliases are last-wins while final node names
are first-wins. Unknown Clash fields, original policy sections and byte-preserved
Clash inputs must not be replaced with reconstructed equivalents.

The explicit offline entrypoint is test-converter-node-offline.ps1. Independent
old-source goldens, strict native comparisons and graph/partial-mutation/fault
checks supplement all earlier regressions. Android is compiled only. Kernel
readiness/ownership/privacy admission remains the next application-workflow
package. Go platform/Mihomo/codec resources and safe fallback remain intentional.

The converter wire ABI is version 2. Lowercase decision facts are computed by
Go's exact strings.ToLower mapping (including U+0130), not recomputed with Rust's
expanding Unicode lowercase. Older wire versions are rejected before effects and
use the Go path; no partially-mutated graph is retried. Independent Unicode
regressions check zero native recovery rather than only equivalent final output.

# Adaptive DNS and independent health confirmation (Phase 38)

This remains a compatibility migration: default Go, opt-in Windows amd64 Rust
sessions, original Go resource identities and trusted non-replaying continuation.
No deployed runtime, VPN connection, proxy, route or system DNS is changed by the
source migration or the offline tests.

Adaptive DNS planning includes DoH exchange validation, resolve head-start race,
upstream check/selection, CAS switching, monitor, serving and relay lifecycle.
The original 500ms head start, 5s contexts and one-minute patrol are preserved.
Resolve returns the first successful reply without joining late workers; checks
consume both completions before selection. Go keeps DNS message codecs, HTTP,
channels, context/timer/listener/server resources and atomic preferred state.

Health confirmation preserves custom-hook precedence, blocked/kernel/port checks,
ordered independent targets and platform timeout. A successful independent path
prevents blocking after a bad bulk measurement. Failed confirmation permits only
the original same-region REJECT operation and ordered state/event publication.
Authority errors are not evidence that traffic has failed.

Android resolver planning preserves Add-before-modulo round-robin, original
upstream order, 2500ms per dial and first-success/last-error identity. Actual
context, atomic counter and connection resources remain Go. Android verification
is compilation only, not executing a resolver or changing device configuration.

The explicit offline entrypoint is test-adaptive-resolver-offline.ps1. Tests use
fake HTTP/dial/OS effects and controlled channel/clock/temporary resources. No
real node, external DNS, listener or health endpoint is exercised. Conversion and
merging closure plus kernel readiness/privacy admission remain later work; this
is not a standalone Rust server or production cutover.

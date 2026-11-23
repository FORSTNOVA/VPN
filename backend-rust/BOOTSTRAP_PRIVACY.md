# Bootstrap ownership and log privacy workflows (Phase 37)

The default remains Go. On Windows amd64 a `rustcore` build may select
`SMARTVPN_RUST_BOOTSTRAP_FLOW=1` and `SMARTVPN_RUST_PRIVACY_LOG=1` with the explicitly
selected core DLL. Missing/disabled exports use the Go oracle; a failed native
session continues from the trusted effect history without replaying effects.
This is not a standalone Rust service or a production cutover.

## Bootstrap boundary

Native planning covers discovery/probing, executable identity/takeover selection,
file-lock retry and handoff, instance double checks, proxy owner verification and
publication/conditional cleanup, Windows/Android startup and stop/JNI-loss ordering,
and portable/record file decisions. Existing lifecycle workflows remain explicit
child usecases. Go holds HTTP bodies, file locks, registry keys, servers, channels,
JNI/FD ownership and resource graphs, plus individual OS/codec operations.
Opaque credentials never become semantic native state. A platform call cannot be
replayed just because the following ABI acknowledgement failed. Runtime platform
behavior remains unaccepted until an explicitly authorized real-environment test.

## Log privacy boundary

Native planning covers subscription secret filtering/deduplication, ordered byte
redaction, newest-line tail selection, replacement counts, temp write/rename and
summary/error ordering. Byte payloads cross the ABI via hex, preserving invalid
UTF-8. Go retains files, output writers and original error objects. Failed temp
write or rename deliberately preserves the original temporary-file behavior;
there is no invented cleanup. Short write without an error preserves the original
writer return semantics. An empty secret uses controlled continuation for Go Rune
replacement semantics rather than claiming equivalent native execution.

## Offline acceptance

Run `test-bootstrap-privacy-offline.ps1` in a child PowerShell process. It wraps
all prior explicitly allowlisted suites and adds actual-native, disabled and
missing-DLL groups. Independent matrices use only fake platform/HTTP callbacks,
controlled channels and isolated temporary files. No real service, proxy owner,
process probe, registry, VPN connection or user log is exercised. The store suite
uses a separate temporary database; Android checks are compile-only.

Adaptive DNS racing/monitoring and health independent-confirmation/block subflows
remain a subsequent migration. Real Windows/Android and VPN/network acceptance is
not implied by these tests.

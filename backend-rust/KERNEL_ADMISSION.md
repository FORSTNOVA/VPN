# Kernel admission and embedded lifecycle (Phase 40)

Default Go and opt-in Windows amd64 rustcore planners remain. Selectors are
SMARTVPN_RUST_KERNEL_ADMISSION_FLOW and SMARTVPN_RUST_EMBEDDED_KERNEL. No process,
VPN, routes, driver, user database or deployed application is changed by tests.

The main planner covers saved/bundled/home path selection, runner construction,
running presence, Start/watch/Stop, readiness retries, port-owner row selection,
privacy admission and config preflight. Actual command/process/log/connection/
clock/channel/interface objects remain Go resources. Original watcher closes the
runtime k.exited before its callback, and Kill failure does not wait. Readiness
preserves 300ms dial/50ms retry and exact monotonic-time ordering; preflight keeps
its original 300-byte final-line truncation and error formatting.

Unknown interruption may clean up only a newly started command that this invocation
positively owns and has not handed to a watcher. It does not kill an old runner or
replay an uncertain spawn, Kill, Wait or callback. This guard is an explicit abnormal
resource-safety addition, not a change to normal lifecycle ordering.

The embedded planner covers Android start/stop/running/refusal/logging/events and
config check/apply ordering. Go retains Mihomo parser/executor, log subscription,
FD and mutex resources. Logging-open failure preserves prior TUN error; only the
first matching event is captured. Stop snapshots/nils, unsubscribes then closes.
The original partial-failure/panic semantics remain; no global Android transaction
rollback is claimed. Long log streams keep fixed native state and can continue
from an interrupted session without truncation or replay.

Tests inject fake command/dial/route/hub/FD/file effects, with controlled channels,
mutexes and isolated temporary files only. IPv6 error classification is additionally
pure-tested from raw/wrapped sentinel errors. No actual netstat, process start/kill,
interface/route query, Mihomo startup, JNI or descriptor operation is exercised.
Android is compiled, not run. The complete offline runner is
`test-kernel-admission-offline.ps1`.

Final source audit identified remaining traffic presentation/history-normalization
and small graph metadata policies; these are not marked complete by this package.
Standalone Rust hosting, Android runtime linkage, native default activation,
packaging/deployment and real-device/network acceptance remain separate boundaries.

# Speed measurement and TUN reporting boundary

Speed workflow includes request limits, kernel-check preparation, active node selection, separate download/upload phases, concurrent workers, byte/time observations, result assembly and cleanup. Rust plans four download and two upload streams; Go owns goroutines, transport/context/body resources, buffers and original atomic observations. The first byte clock uses the original UnixNano semantics. Download failure still permits upload. Responses precede temporary kernel cleanup and unlock. No live speed endpoint was used in validation.

TUN reporting covers Windows/Android status and checks, ordered platform facts, DNS/node/exit classifications and report projection. Go retains individual filesystem, PowerShell, DNS/HTTP and JNI facts, codecs, descriptor ownership and resource locks. Reports preserve their original typed Go projections after native result validation. HTTP ownership spans the write and releases even on writer panic.

Native workflows remain opt-in. Default Go, disabled or missing DLL paths remain supported. Native protocol/limit failures may continue from a trusted mirror without repeating completed effects; uncertain effect failures fail closed. Native TUN strings are UTF-8; invalid bytes and bounded replay limits require the Go continuation, not relaxed parsing.

Wintun driver installation/signature/file replacement, Android FD grant/handOver/release, and remaining HTTP composition are not represented as completed by this phase. Android builds are compile-only, not device acceptance. No actual VPN connection, routing, DNS, proxy or user database changes are part of this migration verification.

# Application control and driver installation workflows

Application workflows cover state projection, health snapshot counts/pool, health parameter submission, patrol requests, event history and patrol start/stop/loop decisions. Existing native health sweep and persistent authority are reused. Go retains mutexes, channels/tickers, store/platform observations, the original Chain/Chains graph and time objects. The original patrol loop has no new generation gate or initial immediate tick.

Windows driver workflows cover source selection, bounded official download/archive inspection, architecture/signature decisions, temporary file installation and response. Local source files retain their original verification policy; official archive and DLL hashes remain distinct. Go retains narrow HTTP/archive/hash/filesystem/process resources. No actual Wintun installation, signature process or live download is part of validation.

The default remains Go. Native effects are checked against a trusted continuation; completed effects are not repeated after ABI faults. HTTP output keeps its original lock lifetime and writer failures after response start propagate without another response. Partial commits, target re-observation and failure-specific temporary-file cleanup remain compatible.

Android compilation is not device acceptance. FD/JNI ownership and platform resources remain Go. Subscription HTTP composition, policy and diagnostics are a separate remaining migration scope.

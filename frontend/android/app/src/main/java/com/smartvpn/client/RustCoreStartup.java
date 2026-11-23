package com.smartvpn.client;

import java.io.File;
import java.nio.charset.StandardCharsets;

/** Process-local startup policy. No Android, JNI, filesystem reads or VPN work. */
final class RustCoreStartup {
    interface NativeCalls {
        int configure(byte[] path, boolean enabled);
        String start(String dataDir, String bridgeClass);
    }
    private boolean configured;
    private boolean uncertain;
    private String status = "go_default";

    synchronized String start(boolean enabled, String nativeLibraryDir,
            String dataDir, String bridgeClass, NativeCalls calls) {
        if (uncertain) {
            throw new IllegalStateException("Native configuration outcome uncertain; restart the process");
        }
        if (!configured) {
            // Consume before crossing JNI: an exception may follow a side effect.
            configured = true;
            if (enabled) {
                if (nativeLibraryDir == null || nativeLibraryDir.indexOf('\0') >= 0
                        || !new File(nativeLibraryDir).isAbsolute()) {
                    status = "go_fallback_invalid_bundle_directory";
                } else {
                    byte[] path = new File(nativeLibraryDir, "libsmartvpn_core.so")
                            .getPath().getBytes(StandardCharsets.UTF_8);
                    try {
                        int result = calls.configure(path, true);
                        status = result == 0 ? "rust_requested"
                                : result == -2 ? "configuration_frozen"
                                : "go_fallback_configure_" + result;
                    } catch (UnsatisfiedLinkError unavailable) {
                        // Old Go libraries have no configuration JNI export.
                        status = "go_fallback_legacy_library";
                    } catch (RuntimeException | Error failure) {
                        uncertain = true;
                        status = "configuration_uncertain";
                        throw failure;
                    }
                }
            }
        }
        // Preserve the original idempotent start contract, including restart
        // after stop. Never retry start in response to configuration failure.
        return calls.start(dataDir, bridgeClass);
    }

    synchronized String status() { return status; }
}

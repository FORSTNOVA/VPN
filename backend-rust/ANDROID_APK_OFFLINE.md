# Offline Android APK staging

`package-android-apk-offline.ps1` requires a new external build root. It never invokes pub, installs an APK, runs adb, or starts Android code. Gradle always uses `--offline`. Existing package configuration and graph are relocated after checking local cache versions against the existing lockfile. This is not a new dependency-resolution verification.

All input directories are protected from output nesting. Existing outputs and reparse ancestors are rejected; copied SDK/cache trees reject reparse entries and robocopy also uses `/XJ`. Source copying excludes binaries, local settings, environment files, credentials and signing keys.

A pre-copy snapshot records original and overlay separately, including overwritten originals and dependency configuration. Copy and final audits verify unchanged inputs. The staged Gradle file must exactly equal the overlay plus the known temporary-key, native-byte-preservation and unminified-release suffix. No general Gradle audit exemption exists.

The Go build explicitly sets `smartvpnRustCoreEnabled=false`; the Rust build explicitly sets true. Cached release engine artifacts require release variants. These unminified APKs use a new isolated short-lived test key, not production signing. The signer certificate SHA-256 must match the run's exported DER certificate.

Checks cover decoded manifest, DEX markers, single-ABI Flutter, Dart and native libraries matching the staging manifest (arm64-v8a or x86_64), byte-exact native artifacts, BuildConfig, signature and 16-KiB zip alignment. Only after both variants and final source checks pass is `apk-success-manifest.json` created. `runtimeValidated=false` remains mandatory: Android loading, JNI execution and VPN behavior are untested.

Run `python test_android_apk_staging_guards.py` for offline negative tests. Reparse tests mock the predicate without creating junctions or changing permissions. Failed roots retain their logs and are not resumed or treated as final artifacts.

Java's sandbox permission probe may require standard execution approval for offline Gradle on the isolated included build. Never alter ACLs, patch Gradle, enable downloads or run a dependency resolver to bypass a failure.

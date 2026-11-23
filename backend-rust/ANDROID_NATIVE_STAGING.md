# Offline Android native staging

This script builds and inspects libraries. It does NOT package or install an APK,
run adb, load an Android library, start the service, or change frontend/jniLibs.
An explicit fresh OutDir is mandatory; existing output is never overwritten.
All Cargo/Go dependency retrieval is disabled. Optional GoCache reuses an explicitly
chosen build cache; otherwise a new isolated cache is created under OutDir. Installed Rust Android targets,
NDK 27.2, Go, and populated dependency caches must already exist.

Select one ABI per staging directory with `-Abi arm64-v8a` (default) or `-Abi x86_64`. The paths below illustrate the default. For x86_64 the script selects the matching Go, Rust and NDK targets and checks ELF64/X86-64.

For x86_64, include the pinned `github.com/mattn/go-sqlite3` module in the
offline Go cache. The NDK compiles its bundled SQLite into the JNI library.

Both Android build scripts apply `backend/android-tun/overlay.ps1` to the
pinned sing-tun module. The two replacement files are modified portions of
sing-tun v0.4.24, copyright 2022 nekohasekai, licensed under GPL-3.0-or-later
(see `licenses/GPL-3.0.txt`). The overlay verifies the upstream files before
building. Use it when compiling Android diagnostic tests with `with_gvisor`.

Example from the project root (adjust explicit paths to your offline tools):

```powershell
./backend-rust/package-android-native.ps1 `
  -OutDir C:/Users/21654/SmartVPN-Android-native-staging-001 `
  -GoExe D:/codebuddythink/vpn/.tools/go/bin/go.exe `
  -GoPath D:/codebuddythink/vpn/.tools/gopath `
  -CargoHome C:/Users/21654/SmartVPN-Rust-Phase6/cargo-home `
  -GoCache C:/Users/21654/AppData/Local/go-build `
  -NdkRoot C:/Users/21654/AppData/Local/Android/Sdk/ndk/27.2.12479018
```

## Output and verification

* `native/arm64-v8a/libsmartvpn.so`: Go JNI library with cmfa, with_gvisor,
  rustcore tags. These first two tags remain mandatory for the Android kernel.
* `native/arm64-v8a/libsmartvpn_core.so`: Rust cdylib, SONAME matches its filename.
* `native/arm64-v8a/libsmartvpn_host.so`: optional in-process monitor with versioned start and stop entry points. Use a Cargo cache populated for the host lockfile as well as the core.
* `rollback-go/arm64-v8a/libsmartvpn.so`: same source, Go-only JNI library without
  rustcore. Keep separate; never merge both libsmartvpn.so files into one package.
* `manifest.json`: SHA256, size, required exported symbols, ELF NEEDED libraries,
  compiler versions and explicit `runtimeValidated=false`.
* `*.readelf.txt`: ELF64 machine for the selected ABI, dynamic exports/dependencies and segment alignment
  evidence. All libraries are linked for at least 16 KiB LOAD alignment.
* `sources.sha256` records production inputs, checked unchanged across the build.
* Build logs, isolated Cargo target and build evidence remain for reproducibility/audit.

The script rejects missing JNI/Rust exports, wrong ELF architecture, unexpected
unpackaged dependencies, and sub-16-KiB LOAD alignment. This is static validation,
not proof of Android linker namespace accessibility, APK zip alignment, JNI
behavior, page-size device compatibility, or functioning VPN/network operations.
Reproducible here means an explicit repeatable offline build recipe, not a promise
of bit-identical bytes across toolchains or build directory paths.

## Integration contract (not performed by this script)

The native and rollback Go libraries now export `SmartVPNRustCoreConfigure(path,
enabled)` and `SmartVPNRustCoreConfigureN(path, length, enabled)` for an explicit
host startup integration. No existing Java invocation is added by this recipe.
ConfigureN is the length-aware entry which can reject embedded NUL. The loader
requires an absolute path with basename libsmartvpn_core.so. Configure before
first core attempt or service start: successful configuration is one-shot;
late/repeated configuration returns -2, and rollback rejects enabled=1 with -4.
Configuration itself does not load the library or start the VPN. Native opt-in
also enables the bridge's selected flow flags; validate this contract on-device.
These exported functions are statically inspected but NEVER executed here.

The existing CoreBridge.java only calls `System.loadLibrary("smartvpn")`.
The existing android.ps1 builds only the Go library and invokes Flutter packaging.
Neither currently opts in to Rust. A reviewed packaging change must include both
native files for arm64-v8a AND supply the loader an explicit absolute path through the startup ConfigureN
API (the loader also recognizes `SMARTVPN_RUST_CORE_LIB`, with legacy
`SMARTVPN_RUST_CORE_DLL` compatibility).
The path must identify the accessible installed/extracted libsmartvpn_core.so,
for example nativeLibraryDir/libsmartvpn_core.so, BEFORE first native lookup.
Do not assume a .so inside an unextracted APK has such a filesystem path.

Loading is optional explicit `dlopen(RTLD_NOW|RTLD_LOCAL)`, not a hard
DT_NEEDED dependency from libsmartvpn.so. Unset configuration means Go fallback.
Failure to load or missing capabilities must retain the implementation's existing
safe selection semantics; environment-only path selection and startup Configure opt-in are different;
Configure explicitly enables its flow opt-ins, while environment-only paths must
also set the intended selectors. No production startup configuration is changed here.

Rollback packaging uses only rollback-go/libsmartvpn.so and no Rust opt-in.
Before deployment, independently test APK native-library packaging/alignment,
Android loading, required capabilities, JNI startup/shutdown, and runtime behavior
on a disposable device/emulator. No such device/runtime validation was performed
by this staging script, and its output must not be described as a tested APK.

The symbol inventory excludes the separate optional smartvpn_store_* DLL ABI;
this recipe does not build, package, or enable a Rust store backend.

Output safety: the whole project subtree, frontend/jniLibs paths, existing output,
and every existing reparse-point ancestor are rejected before mkdir. Guard tests
cover nine path cases and a synthetic Get-Item reparse fact; real junction
creation was not permitted by the test sandbox, so that case is not claimed as
a real-filesystem junction test.

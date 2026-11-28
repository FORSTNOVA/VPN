# Android system VPN acceptance

Use a dedicated Android Studio AVD, a fresh SmartVPN profile
and no active VPN. The test changes this Android user's VPN grant. The Windows
VPN and host network are independent of the emulator test.

Check the AVD name with `adb -s $serial emu avd name` and the installed ABI
with `dumpsys package com.smartvpn.client`. Phase 69 used the Android 16
API 36 Google APIs x86_64 system image with ARM64 application translation.

For the descriptor regression, set the Android Go target, cgo and NDK compiler
as in the native staging build. From `backend`, run the following with
absolute paths for the Go executable, overlay and test output:

```powershell
./android-tun/overlay.ps1 -GoExe $go -OutFile $overlay
& $go test "-overlay=$overlay" -tags cmfa,with_gvisor -c -o $test ./diagnostics/androidtun
adb -s $serial push $test /data/local/tmp/smartvpn-lifetime.test
adb -s $serial shell chmod 700 /data/local/tmp/smartvpn-lifetime.test
adb -s $serial shell '/data/local/tmp/smartvpn-lifetime.test -test.v -test.count=20'
```

Install a Rust-enabled APK containing the core and host shared libraries. Build
the instrumentation APK with the same test signing key. On Windows, set
`SMARTVPN_TEST_STOREPASS` in the process environment and run:

```powershell
./build-runner.ps1 -AndroidSdk $sdk -Jdk $jdk -TestKeyStore $key -OutDir $newOutput
adb -s $serial install -r $rustApk
adb -s $serial install "$newOutput/runner.apk"
adb -s $serial shell appops set com.smartvpn.client ACTIVATE_VPN allow
adb -s $serial shell appops set com.smartvpn.acceptance ACTIVATE_VPN allow
adb -s $serial shell am instrument -w -r com.smartvpn.acceptance/.DeviceRun
```

Check the final instrumentation result for `INSTRUMENTATION_CODE: -1`; an adb
exit code of zero alone also occurs on assertion failures. VPN consent is
pre-granted for automation. The peer Activity runs under its own UID and
requires the shell DUMP permission to launch. It verifies VPN transport and
a random TCP request/reply via Mihomo, then underlying transport after release.
The VLESS fixture only listens on device loopback and never forwards traffic.
The revoke cycle uses the second package's real VpnService.prepare call.

From Phase 73, the lifecycle run also checks a fresh service-log entry for
each observed TUN descriptor to verify that Rust executed the handover. Use
the matching Rust core and JNI library when running this version of the suite.

The runner creates and removes one randomly named manual node. It checks three
sessions (ordinary stop, system revoke, reconnect and stop) in one process.
The OS can retain a DOWN interface after revoke on the translation emulator.
Acceptance requires the client descriptor to be gone, the Rust worker to have
exited, the peer to be back on its underlying network and the next tunnel to
carry traffic. The original consent dialog, physical radios and public proxy
performance are separate tests. After running on a fresh test profile, return
the client VPN app-op to default and uninstall the acceptance package.

For repeated crash investigations, keep the matching signed client and runner
installed, pre-grant both packages as above, and select the device explicitly:

```powershell
./run-series.ps1 -Adb $adb -Serial $serial -Runs 12 -OutDir $newOutput
```

The script records the installed APK hash, selected ABI, complete instrumentation
result and client-UID runtime log for each run. Failed runs also retain Android
process exit history. The script returns an error if any run fails. A missing
fatal signature alone cannot establish that a failed run passed. On a physical
phone, use a dedicated test profile with a compatible test signature.

To verify store compatibility through an APK upgrade on the test AVD, run
`DeviceRun` with a unique UUID in `-e storeFixture` and `-e storeStep seed`
on the original client. Install the replacement with `adb -s $serial install
-r --abi x86_64 $apk`. Run `storeStep remove` and then `storeStep check-absent`
in separate instrumentation invocations using the same UUID. Each step must
finish with code -1. The fixture uses a loopback address and the store test
does not require a VPN grant.
### Live subscription merge

Pass `-TestLiveMerge` to `run-series.ps1` on a disposable emulator with an
empty subscription catalog. The runner adds a loopback subscription, merges
while connected, selects its node and checks the descriptor before the
cross-UID traffic challenge. Use a read-only AVD or a disposable data image
because merging also updates the active profile and selected node.

### Per-node Mux and QUIC keepalive

Install the ARM64 client and the matching instrumentation APK on a disposable
Android Studio AVD and grant SmartVPN permission to establish a VPN. The runner
overwrites its test chain, manual nodes, app groups and selected node. With
Python 3.11+, cryptography and sing-box 1.14.6 installed, run a case from this
directory:

```powershell
python session_fixture.py --adb $adb --serial $serial --sing-box $singBox --protocol vmess --mode mux --out $results
```

Use separate result directories for the VMess `default` control, TCP `mux`
cases (`vmess`, `vless`, `trojan`, `shadowsocks`), and QUIC cases
(`hysteria2` or `tuic`, with `default` or `keepalive`). The isolated chain
routes only the peer app through the selected entry and a SOCKS5 exit. It sends
random payloads on six concurrent TCP connections, checks every reply, closes
all six sockets, waits 40 seconds and opens a seventh. Observe the outer
handshakes at the first hop. QUIC Initial retransmissions are grouped by the
client source connection ID and socket. The JSON output includes handshake
times and the direction of traffic that wakes an idle QUIC transport.

Pass `--second-hop` for a TCP Mux case to place the tested node after SOCKS5.

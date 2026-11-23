# Android startup offline contract test

Run `backend-rust/test-android-startup.ps1` with explicit absolute `-Javac` and `-Java` executable paths and a fresh `-OutDir` beneath the current process TEMP directory. Output must be outside the project. Existing outputs and reparse-point ancestors are refused; no cleanup/deletion is performed.

Example (substitute installed trusted JDK paths):

```powershell
./backend-rust/test-android-startup.ps1 -Javac 'C:\jdk\bin\javac.exe' -Java 'C:\jdk\bin\java.exe' -OutDir (Join-Path $env:TEMP ('SmartVPN-startup-'+[guid]::NewGuid().ToString('N')))
```

The test has 13 scenario runs: default disabled (1), configuration statuses 0/-1/-2/-3/-4 (5, each with repeated startup), missing legacy JNI (1), invalid directory null/relative/NUL (3), and 32 concurrent calls (1), and side-effect-then-throw RuntimeException/Error (2). These make 47 fake start calls and 9 fake configure calls in total. Assertions also verify fixed library filename, ordering, one-time configuration, exact status labels, and no duplicate startup on fallback. It tests the platform-independent Java policy, not the Android Context wrapper or native execution.

Only javac/java run; fake callbacks replace JNI. There is no app deployment, filesystem configuration read, network, FD or VPN operation. Successful `rust_requested` means requested configuration, not proof the Rust library loaded. Android shared-library/APK compilation and real-device runtime validation are separate gates.

Unknown configuration exceptions consume the attempt before crossing JNI, record configuration_uncertain, and block subsequent startup until process restart. No automatic configuration retry or Go-fallback claim is made for an uncertain outcome.

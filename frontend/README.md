# SmartVPN Flutter client

This directory contains the shared Windows and Android Flutter interface. The Windows runner also hosts the tray icon and the native topmost Amiya desktop-pet window; Android uses the VpnService bridge.

## Build

From this directory, run `..\.tools\flutter\bin\flutter.bat pub get`, then `..\.tools\flutter\bin\flutter.bat build windows --release` for Windows. Use `..\android.ps1` from the repository root to build the Android app and its embedded Go service.
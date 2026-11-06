# SmartVPN Flutter client

This directory contains the shared Windows and Android Flutter interface. The Windows runner also hosts the tray icon and the native topmost Amiya desktop-pet window; Android uses the VpnService bridge.

## Amiya desktop companion

The Windows pet uses a transparent four-pose atlas (`assets/amiya_pet_atlas.png`): idle, blink, greeting and tablet. At rest, only the character and a tiny decorative diamond remain visible: cyan when connected, muted gray when disconnected, and amber when blocked. No connection text, node names or rates appear until hovering, which reveals the telemetry card above Amiya. Click to greet, drag to reposition, double-click to restore the main window, or right-click for VPN controls and settings.

Fullscreen applications trigger a compact strip; leaving fullscreen restores the normal desktop position. Position, scale, compact dimensions and visibility are stored in the existing Windows pet preferences. The settings dialog applies changes only on Apply; Cancel discards edits. Disabling the pet stops its two native timers. Rendering is separated into `desktop_pet_renderer.cpp` for deterministic visual checks.

## Build commands

From this directory, run `..\.tools\flutter\bin\flutter.bat pub get`, then `..\.tools\flutter\bin\flutter.bat build windows --release` for Windows. Use `..\android.ps1` from the repository root to build the Android app and its embedded Go service.

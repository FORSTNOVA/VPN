$ErrorActionPreference='Stop'
$env:CARGO_HOME='C:\Users\21654\.gemini\antigravity\brain\6a453053-a315-41e2-b122-010410e5b9fd\scratch\cargo-home'
$env:TEMP=$env:TMP=if ($env:SMARTVPN_TEST_TMP) { $env:SMARTVPN_TEST_TMP } else { 'C:\Users\21654\.gemini\antigravity\brain\6a453053-a315-41e2-b122-010410e5b9fd\scratch' }
$env:CARGO_TARGET_DIR="$PSScriptRoot/.build"
Set-StrictMode -Version Latest
function Check-Exit([string]$step) {
    if ($LASTEXITCODE -ne 0) { throw "$step failed (exit $LASTEXITCODE)" }
}
$env:RUSTC_BOOTSTRAP = "1"

$env:AWS_LC_SYS_PREBUILT_NASM="1"
$env:CARGO_TARGET_X86_64_PC_WINDOWS_MSVC_RUSTFLAGS="--cfg tokio_unstable"

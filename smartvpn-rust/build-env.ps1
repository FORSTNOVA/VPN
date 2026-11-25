$ErrorActionPreference='Stop'
if ($env:SMARTVPN_TEST_TMP) { $env:TEMP=$env:TMP=$env:SMARTVPN_TEST_TMP }
$env:CARGO_TARGET_DIR="$PSScriptRoot/.build"
Set-StrictMode -Version Latest
function Check-Exit([string]$step) {
    if ($LASTEXITCODE -ne 0) { throw "$step failed (exit $LASTEXITCODE)" }
}
$env:RUSTC_BOOTSTRAP = "1"

$env:AWS_LC_SYS_PREBUILT_NASM="1"
$repoRoot = Split-Path $PSScriptRoot
$cargoCache = if ($env:CARGO_HOME) { $env:CARGO_HOME } else { Join-Path $env:USERPROFILE '.cargo' }
$env:CARGO_ENCODED_RUSTFLAGS = @('--cfg', 'tokio_unstable', "--remap-path-prefix=$repoRoot=/src", "--remap-path-prefix=$env:USERPROFILE=/home/builder", "--remap-path-prefix=$cargoCache=/cargo") -join [char]31

param(
 [Parameter(Mandatory=$true)][string]$GoExe,
 [Parameter(Mandatory=$true)][string]$GoPath,
 [string]$Cache=(Join-Path $env:TEMP 'smartvpn-go-parity-cache'),
 [string]$RustTargetDirectory=(Join-Path $PSScriptRoot 'target')
)
$ErrorActionPreference='Stop'
# Invoke in a child pwsh process. All environment changes below are process-only.
& (Join-Path $PSScriptRoot 'test-offline.ps1') -GoExe $GoExe -GoPath $GoPath -Cache $Cache -RustTargetDirectory $RustTargetDirectory
if($LASTEXITCODE -ne 0){throw 'Core regression suite failed'}
$backend=Join-Path (Split-Path $PSScriptRoot -Parent) 'backend'
$env:SMARTVPN_RUST_CORE_DLL=Join-Path $RustTargetDirectory 'release\smartvpn_core.dll'
$env:SMARTVPN_RUST_HEALTH_FLOW='1'
Push-Location $backend
try {
 # These prefixes belong exclusively to fake-effect/continuation tests.
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestHealthFlow|TestHealthService|TestMigrationHealthFlow)' .
 if($LASTEXITCODE -ne 0){throw 'Health continuation offline suite failed'}
 $env:SMARTVPN_RUST_HEALTH_FLOW='0'
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^TestHealthService' .
 if($LASTEXITCODE -ne 0){throw 'Explicitly disabled health flow regression failed'}
 $env:SMARTVPN_RUST_HEALTH_FLOW='1'
 $env:SMARTVPN_RUST_CORE_DLL=Join-Path $env:TEMP ('missing-health-flow-'+[guid]::NewGuid().ToString()+'.dll')
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^TestHealthService' .
 if($LASTEXITCODE -ne 0){throw 'Missing health DLL fallback regression failed'}
} finally {Pop-Location}


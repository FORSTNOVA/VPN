param(
 [Parameter(Mandatory=$true)][string]$GoExe,
 [Parameter(Mandatory=$true)][string]$GoPath,
 [string]$Cache=(Join-Path $env:TEMP 'smartvpn-go-parity-cache'),
 [string]$RustTargetDirectory=(Join-Path $PSScriptRoot 'target')
)
$ErrorActionPreference='Stop'
& (Join-Path $PSScriptRoot 'test-region-job-offline.ps1') -GoExe $GoExe -GoPath $GoPath -Cache $Cache -RustTargetDirectory $RustTargetDirectory
if($LASTEXITCODE -ne 0){throw 'Prior migration regression suite failed'}
$env:SMARTVPN_RUST_CORE_DLL=Join-Path $RustTargetDirectory 'release\smartvpn_core.dll'
$env:SMARTVPN_RUST_MANUAL_FLOW='1'
Push-Location (Join-Path (Split-Path $PSScriptRoot -Parent) 'backend')
try {
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestManualFlow|TestManualService|TestMigrationManual)' .
 if($LASTEXITCODE -ne 0){throw 'Manual workflow offline suite failed'}
 $env:SMARTVPN_RUST_MANUAL_FLOW='0'
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestManualService|TestMigrationManualSafe|TestMigrationManualHTTP)' .
 if($LASTEXITCODE -ne 0){throw 'Disabled manual workflow regression failed'}
 $env:SMARTVPN_RUST_MANUAL_FLOW='1'
 $env:SMARTVPN_RUST_CORE_DLL=Join-Path $env:TEMP ('missing-manual-flow-'+[guid]::NewGuid().ToString()+'.dll')
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestManualService|TestMigrationManualSafe|TestMigrationManualHTTP)' .
 if($LASTEXITCODE -ne 0){throw 'Missing manual workflow DLL fallback regression failed'}
} finally {Pop-Location}


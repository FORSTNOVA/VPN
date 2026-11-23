param(
 [Parameter(Mandatory=$true)][string]$GoExe,
 [Parameter(Mandatory=$true)][string]$GoPath,
 [string]$Cache=(Join-Path $env:TEMP 'smartvpn-go-parity-cache'),
 [string]$RustTargetDirectory=(Join-Path $PSScriptRoot 'target')
)
$ErrorActionPreference='Stop'
& (Join-Path $PSScriptRoot 'test-path-offline.ps1') -GoExe $GoExe -GoPath $GoPath -Cache $Cache -RustTargetDirectory $RustTargetDirectory
if($LASTEXITCODE -ne 0){throw 'Prior migration regression suite failed'}
$env:SMARTVPN_RUST_CORE_DLL=Join-Path $RustTargetDirectory 'release\smartvpn_core.dll'
$env:SMARTVPN_RUST_PATH_MONITOR_FLOW='1'
Push-Location (Join-Path (Split-Path $PSScriptRoot -Parent) 'backend')
try {
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestPathMonitorFlow|TestPathMonitorService|TestMigrationPathMonitor)' .
 if($LASTEXITCODE -ne 0){throw 'Path monitor workflow offline suite failed'}
 $env:SMARTVPN_RUST_PATH_MONITOR_FLOW='0'
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestPathMonitorService|TestMigrationPathMonitorSafe|TestMigrationPathMonitorHTTP)' .
 if($LASTEXITCODE -ne 0){throw 'Disabled path monitor workflow regression failed'}
 $env:SMARTVPN_RUST_PATH_MONITOR_FLOW='1'
 $env:SMARTVPN_RUST_CORE_DLL=Join-Path $env:TEMP ('missing-path-monitor-flow-'+[guid]::NewGuid().ToString()+'.dll')
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestPathMonitorService|TestMigrationPathMonitorSafe|TestMigrationPathMonitorHTTP)' .
 if($LASTEXITCODE -ne 0){throw 'Missing path monitor workflow DLL fallback regression failed'}
} finally {Pop-Location}


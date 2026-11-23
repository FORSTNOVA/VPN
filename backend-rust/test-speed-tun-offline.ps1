param(
 [Parameter(Mandatory=$true)][string]$GoExe,
 [Parameter(Mandatory=$true)][string]$GoPath,
 [string]$Cache=(Join-Path $env:TEMP 'smartvpn-go-parity-cache'),
 [string]$RustTargetDirectory=(Join-Path $PSScriptRoot 'target')
)
$ErrorActionPreference='Stop'
$env:SMARTVPN_RUST_SPEED_FLOW='1'
$env:SMARTVPN_RUST_TUN_REPORT='1'
& (Join-Path $PSScriptRoot 'test-china-offline.ps1') -GoExe $GoExe -GoPath $GoPath -Cache $Cache -RustTargetDirectory $RustTargetDirectory
if($LASTEXITCODE -ne 0){throw 'Prior China migration regression suite failed'}
$env:SMARTVPN_RUST_CORE_DLL=Join-Path $RustTargetDirectory 'release\smartvpn_core.dll'
$env:SMARTVPN_RUST_SERVICE_STATE='1'
$env:SMARTVPN_TEST_EXPECT_DOCUMENT_RUST='1'
Push-Location (Join-Path (Split-Path $PSScriptRoot -Parent) 'backend')
try {
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestSpeedFlow|TestSpeedService|TestMigrationSpeed|TestTunReport)' .
 if($LASTEXITCODE -ne 0){throw 'Speed/TUN native offline suite failed'}
 $env:SMARTVPN_RUST_SPEED_FLOW='0'
$env:SMARTVPN_RUST_TUN_REPORT='0'
 $env:SMARTVPN_RUST_SERVICE_STATE='0'
 $env:SMARTVPN_TEST_EXPECT_DOCUMENT_RUST='0'
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestSpeedService|TestMigrationSpeed|TestTunReportSafe|TestTunReportIndependentSelected|TestTunReportRealOwnership|TestTunReportContinuation)' .
 if($LASTEXITCODE -ne 0){throw 'Disabled Speed/TUN regression failed'}
 $env:SMARTVPN_RUST_SPEED_FLOW='1'
$env:SMARTVPN_RUST_TUN_REPORT='1'
 $env:SMARTVPN_RUST_SERVICE_STATE='1'
 $env:SMARTVPN_RUST_CORE_DLL=Join-Path $env:TEMP ('missing-speed-tun-'+[guid]::NewGuid().ToString()+'.dll')
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestSpeedService|TestMigrationSpeed|TestTunReportSafe|TestTunReportIndependentSelected|TestTunReportRealOwnership|TestTunReportContinuation)' .
 if($LASTEXITCODE -ne 0){throw 'Missing Speed/TUN DLL fallback regression failed'}
} finally {Pop-Location}




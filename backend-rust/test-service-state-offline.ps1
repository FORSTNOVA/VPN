param(
 [Parameter(Mandatory=$true)][string]$GoExe,
 [Parameter(Mandatory=$true)][string]$GoPath,
 [string]$Cache=(Join-Path $env:TEMP 'smartvpn-go-parity-cache'),
 [string]$RustTargetDirectory=(Join-Path $PSScriptRoot 'target')
)
$ErrorActionPreference='Stop'
& (Join-Path $PSScriptRoot 'test-lifecycle-offline.ps1') -GoExe $GoExe -GoPath $GoPath -Cache $Cache -RustTargetDirectory $RustTargetDirectory
if($LASTEXITCODE -ne 0){throw 'Prior migration regression suite failed'}
$env:SMARTVPN_RUST_CORE_DLL=Join-Path $RustTargetDirectory 'release\smartvpn_core.dll'
$env:SMARTVPN_RUST_SERVICE_STATE='1'
$env:SMARTVPN_TEST_EXPECT_DOCUMENT_RUST='1'
Push-Location (Join-Path (Split-Path $PSScriptRoot -Parent) 'backend')
try {
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestServiceState|TestSettingsDocument|TestHealthSettings|TestStateHTTP|TestControlDocument|TestCatalogResource)' .
 if($LASTEXITCODE -ne 0){throw 'Service state offline suite failed'}
 $env:SMARTVPN_RUST_SERVICE_STATE='0'
 $env:SMARTVPN_TEST_EXPECT_DOCUMENT_RUST='0'
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestServiceState(Safe|Closed|Pending|Lifecycle)|TestSettingsDocument|TestHealthSettings|TestStateHTTP|TestControlDocument|TestCatalogResource)' .
 if($LASTEXITCODE -ne 0){throw 'Disabled service state regression failed'}
 $env:SMARTVPN_RUST_SERVICE_STATE='1'
 $env:SMARTVPN_RUST_CORE_DLL=Join-Path $env:TEMP ('missing-service-state-'+[guid]::NewGuid().ToString()+'.dll')
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestServiceState(Safe|Closed|Pending|Lifecycle)|TestSettingsDocument|TestHealthSettings|TestStateHTTP|TestControlDocument|TestCatalogResource)' .
 if($LASTEXITCODE -ne 0){throw 'Missing service state DLL fallback regression failed'}
} finally {Pop-Location}


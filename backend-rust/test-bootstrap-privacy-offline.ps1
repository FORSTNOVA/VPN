param(
 [Parameter(Mandatory=$true)][string]$GoExe,
 [Parameter(Mandatory=$true)][string]$GoPath,
 [string]$Cache=(Join-Path $env:TEMP 'smartvpn-go-parity-cache'),
 [string]$RustTargetDirectory=(Join-Path $PSScriptRoot 'target')
)
$ErrorActionPreference='Stop'
$env:SMARTVPN_RUST_BOOTSTRAP_FLOW='1'
$env:SMARTVPN_RUST_PRIVACY_LOG='1'
& (Join-Path $PSScriptRoot 'test-subscription-policy-offline.ps1') -GoExe $GoExe -GoPath $GoPath -Cache $Cache -RustTargetDirectory $RustTargetDirectory
if($LASTEXITCODE -ne 0){throw 'Prior subscription/policy migration regression failed'}
$env:SMARTVPN_RUST_CORE_DLL=Join-Path $RustTargetDirectory 'release\smartvpn_core.dll'
$env:SMARTVPN_RUST_SERVICE_STATE='1'
$env:SMARTVPN_TEST_EXPECT_DOCUMENT_RUST='1'
Push-Location (Join-Path (Split-Path $PSScriptRoot -Parent) 'backend')
try {
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestBootstrapActualIndependent|TestBootstrapIndependent|TestBootstrapFlow|TestBootstrapService|TestPrivacyLogFlow|TestPrivacyLogService|TestPrivacyLogOwned|TestPrivacyLogContinuation|TestLogPrivacyIndependent)' .
 if($LASTEXITCODE -ne 0){throw 'Bootstrap/privacy native offline suite failed'}
 $env:SMARTVPN_RUST_BOOTSTRAP_FLOW='0'
 $env:SMARTVPN_RUST_PRIVACY_LOG='0'
 $env:SMARTVPN_RUST_SERVICE_STATE='0'
 $env:SMARTVPN_TEST_EXPECT_DOCUMENT_RUST='0'
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestBootstrapIndependent|TestBootstrapService|TestPrivacyLogService|TestPrivacyLogOwned|TestPrivacyLogContinuation|TestLogPrivacyIndependent)' .
 if($LASTEXITCODE -ne 0){throw 'Disabled bootstrap/privacy regression failed'}
 $env:SMARTVPN_RUST_BOOTSTRAP_FLOW='1'
 $env:SMARTVPN_RUST_PRIVACY_LOG='1'
 $env:SMARTVPN_RUST_SERVICE_STATE='1'
 $env:SMARTVPN_RUST_CORE_DLL=Join-Path $env:TEMP ('missing-bootstrap-privacy-'+[guid]::NewGuid().ToString()+'.dll')
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestBootstrapIndependent|TestBootstrapService|TestPrivacyLogService|TestPrivacyLogOwned|TestPrivacyLogContinuation|TestLogPrivacyIndependent)' .
 if($LASTEXITCODE -ne 0){throw 'Missing bootstrap/privacy DLL fallback regression failed'}
} finally {Pop-Location}

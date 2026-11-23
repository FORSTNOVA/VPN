param(
 [Parameter(Mandatory=$true)][string]$GoExe,
 [Parameter(Mandatory=$true)][string]$GoPath,
 [string]$Cache=(Join-Path $env:TEMP 'smartvpn-go-parity-cache'),
 [string]$RustTargetDirectory=(Join-Path $PSScriptRoot 'target')
)
$ErrorActionPreference='Stop'
$env:SMARTVPN_RUST_ADAPTIVE_FLOW='1'
$env:SMARTVPN_RUST_ANDROID_RESOLVER='1'
& (Join-Path $PSScriptRoot 'test-bootstrap-privacy-offline.ps1') -GoExe $GoExe -GoPath $GoPath -Cache $Cache -RustTargetDirectory $RustTargetDirectory
if($LASTEXITCODE -ne 0){throw 'Prior bootstrap/privacy migration regression failed'}
$env:SMARTVPN_RUST_CORE_DLL=Join-Path $RustTargetDirectory 'release\smartvpn_core.dll'
$env:SMARTVPN_RUST_SERVICE_STATE='1'
$env:SMARTVPN_TEST_EXPECT_DOCUMENT_RUST='1'
Push-Location (Join-Path (Split-Path $PSScriptRoot -Parent) 'backend')
try {
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestAdaptiveActualIndependent|TestAdaptiveIndependent|TestAdaptiveDNSIndependent|TestAdaptiveFlow|TestAdaptiveService|TestAndroidResolverActualIndependent|TestAndroidResolverIndependent|TestAndroidResolverFlow|TestAndroidResolverService)' .
 if($LASTEXITCODE -ne 0){throw 'Adaptive/resolver native offline suite failed'}
 $env:SMARTVPN_RUST_ADAPTIVE_FLOW='0'
 $env:SMARTVPN_RUST_ANDROID_RESOLVER='0'
 $env:SMARTVPN_RUST_SERVICE_STATE='0'
 $env:SMARTVPN_TEST_EXPECT_DOCUMENT_RUST='0'
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestAdaptiveIndependent|TestAdaptiveDNSIndependent|TestAdaptiveService|TestAndroidResolverIndependent|TestAndroidResolverService)' .
 if($LASTEXITCODE -ne 0){throw 'Disabled adaptive/resolver regression failed'}
 $env:SMARTVPN_RUST_ADAPTIVE_FLOW='1'
 $env:SMARTVPN_RUST_ANDROID_RESOLVER='1'
 $env:SMARTVPN_RUST_SERVICE_STATE='1'
 $env:SMARTVPN_RUST_CORE_DLL=Join-Path $env:TEMP ('missing-adaptive-resolver-'+[guid]::NewGuid().ToString()+'.dll')
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestAdaptiveIndependent|TestAdaptiveDNSIndependent|TestAdaptiveService|TestAndroidResolverIndependent|TestAndroidResolverService)' .
 if($LASTEXITCODE -ne 0){throw 'Missing adaptive/resolver DLL fallback regression failed'}
} finally {Pop-Location}

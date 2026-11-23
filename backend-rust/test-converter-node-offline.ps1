param(
 [Parameter(Mandatory=$true)][string]$GoExe,
 [Parameter(Mandatory=$true)][string]$GoPath,
 [string]$Cache=(Join-Path $env:TEMP 'smartvpn-go-parity-cache'),
 [string]$RustTargetDirectory=(Join-Path $PSScriptRoot 'target')
)
$ErrorActionPreference='Stop'
$env:SMARTVPN_RUST_CONVERTER_FLOW='1'
$env:SMARTVPN_RUST_NODE_FRONT='1'
& (Join-Path $PSScriptRoot 'test-adaptive-resolver-offline.ps1') -GoExe $GoExe -GoPath $GoPath -Cache $Cache -RustTargetDirectory $RustTargetDirectory
if($LASTEXITCODE -ne 0){throw 'Prior adaptive/resolver migration regression failed'}
$env:SMARTVPN_RUST_CORE_DLL=Join-Path $RustTargetDirectory 'release\smartvpn_core.dll'
$env:SMARTVPN_RUST_SERVICE_STATE='1'
$env:SMARTVPN_TEST_EXPECT_DOCUMENT_RUST='1'
Push-Location (Join-Path (Split-Path $PSScriptRoot -Parent) 'backend')
try {
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestConverterActualIndependent|TestConverterIndependent|TestConverterFlow|TestConverterService|TestNodeFrontFlow|TestNodeFrontService|TestProfileConversionActualIndependent|TestProfileConversionIndependent)' .
 if($LASTEXITCODE -ne 0){throw 'Converter/node frontend native offline suite failed'}
 $env:SMARTVPN_RUST_CONVERTER_FLOW='0'
 $env:SMARTVPN_RUST_NODE_FRONT='0'
 $env:SMARTVPN_RUST_SERVICE_STATE='0'
 $env:SMARTVPN_TEST_EXPECT_DOCUMENT_RUST='0'
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestConverterIndependent|TestConverterService|TestNodeFrontService|TestProfileConversionIndependent)' .
 if($LASTEXITCODE -ne 0){throw 'Disabled converter/node frontend regression failed'}
 $env:SMARTVPN_RUST_CONVERTER_FLOW='1'
 $env:SMARTVPN_RUST_NODE_FRONT='1'
 $env:SMARTVPN_RUST_SERVICE_STATE='1'
 $env:SMARTVPN_RUST_CORE_DLL=Join-Path $env:TEMP ('missing-converter-node-'+[guid]::NewGuid().ToString()+'.dll')
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestConverterIndependent|TestConverterService|TestNodeFrontService|TestProfileConversionIndependent)' .
 if($LASTEXITCODE -ne 0){throw 'Missing converter/node frontend DLL fallback regression failed'}
} finally {Pop-Location}

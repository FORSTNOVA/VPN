param(
 [Parameter(Mandatory=$true)][string]$GoExe,
 [Parameter(Mandatory=$true)][string]$GoPath,
 [string]$Cache=(Join-Path $env:TEMP 'smartvpn-go-parity-cache'),
 [string]$RustTargetDirectory=(Join-Path $PSScriptRoot 'target')
)
$ErrorActionPreference='Stop'
$env:SMARTVPN_RUST_KERNEL_ADMISSION_FLOW='1'
$env:SMARTVPN_RUST_EMBEDDED_KERNEL='1'
& (Join-Path $PSScriptRoot 'test-converter-node-offline.ps1') -GoExe $GoExe -GoPath $GoPath -Cache $Cache -RustTargetDirectory $RustTargetDirectory
if($LASTEXITCODE -ne 0){throw 'Prior converter/node migration regression failed'}
$env:SMARTVPN_RUST_CORE_DLL=Join-Path $RustTargetDirectory 'release\smartvpn_core.dll'
$env:SMARTVPN_RUST_SERVICE_STATE='1'
$env:SMARTVPN_TEST_EXPECT_DOCUMENT_RUST='1'
Push-Location (Join-Path (Split-Path $PSScriptRoot -Parent) 'backend')
try {
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestKernelAdmissionActualIndependent|TestKernelAdmissionIndependent|TestKernelAdmissionFlow|TestKernelAdmissionService|TestEmbeddedKernelActualIndependent|TestEmbeddedKernelIndependent|TestEmbeddedKernelFlow|TestEmbeddedKernelService)' .
 if($LASTEXITCODE -ne 0){throw 'Kernel admission/embedded native offline suite failed'}
 $env:SMARTVPN_RUST_KERNEL_ADMISSION_FLOW='0'
 $env:SMARTVPN_RUST_EMBEDDED_KERNEL='0'
 $env:SMARTVPN_RUST_SERVICE_STATE='0'
 $env:SMARTVPN_TEST_EXPECT_DOCUMENT_RUST='0'
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestKernelAdmissionIndependent|TestKernelAdmissionService|TestEmbeddedKernelIndependent|TestEmbeddedKernelService)' .
 if($LASTEXITCODE -ne 0){throw 'Disabled kernel admission/embedded regression failed'}
 $env:SMARTVPN_RUST_KERNEL_ADMISSION_FLOW='1'
 $env:SMARTVPN_RUST_EMBEDDED_KERNEL='1'
 $env:SMARTVPN_RUST_SERVICE_STATE='1'
 $env:SMARTVPN_RUST_CORE_DLL=Join-Path $env:TEMP ('missing-kernel-admission-'+[guid]::NewGuid().ToString()+'.dll')
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestKernelAdmissionIndependent|TestKernelAdmissionService|TestEmbeddedKernelIndependent|TestEmbeddedKernelService)' .
 if($LASTEXITCODE -ne 0){throw 'Missing kernel admission/embedded DLL fallback regression failed'}
} finally {Pop-Location}

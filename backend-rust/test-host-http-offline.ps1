param(
 [Parameter(Mandatory=$true)][string]$GoExe,
 [Parameter(Mandatory=$true)][string]$GoPath,
 [string]$Cache=(Join-Path $env:TEMP 'smartvpn-go-parity-cache'),
 [string]$RustTargetDirectory=(Join-Path $PSScriptRoot 'target')
)
$ErrorActionPreference='Stop'
$env:SMARTVPN_RUST_HTTP_DISPATCH='1'
& (Join-Path $PSScriptRoot 'test-projection-config-offline.ps1') -GoExe $GoExe -GoPath $GoPath -Cache $Cache -RustTargetDirectory $RustTargetDirectory
if($LASTEXITCODE -ne 0){throw 'Prior projection/config regression failed'}
$manifest=Join-Path $PSScriptRoot 'host\Cargo.toml'
foreach($args in @(@('fmt','--manifest-path',$manifest,'--check'),@('test','--manifest-path',$manifest,'--offline','--locked'),@('clippy','--manifest-path',$manifest,'--offline','--locked','--all-targets','--','-D','warnings'),@('build','--manifest-path',$manifest,'--release','--offline','--locked'))) {
 & cargo @args
 if($LASTEXITCODE -ne 0){throw 'Isolated Rust host validation failed'}
}
$env:SMARTVPN_RUST_CORE_DLL=Join-Path $RustTargetDirectory 'release\smartvpn_core.dll'
$env:SMARTVPN_RUST_SERVICE_STATE='1'
$env:SMARTVPN_TEST_EXPECT_DOCUMENT_RUST='1'
Push-Location (Join-Path (Split-Path $PSScriptRoot -Parent) 'backend')
try {
 $env:SMARTVPN_TEST_OFFLINE_HOST=(Resolve-Path (Join-Path $RustTargetDirectory 'release\smartvpn-host.exe')).Path
 & $GoExe test -count=1 -timeout=120s -run '^Test(OfflineHostIdleParity|RustHTTPMonitor|ManagedKernelLifecycle|ManagedClientParentExit)$' .
 if($LASTEXITCODE -ne 0){throw 'Rust host and Go idle-handler parity failed'}
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestHTTPDispatchActualIndependent|TestHTTPDispatchIndependent|TestHTTPDispatchService|TestHTTPDispatchFlow)' .
 if($LASTEXITCODE -ne 0){throw 'HTTP dispatch native offline suite failed'}
 $env:SMARTVPN_RUST_HTTP_DISPATCH='0'
 $env:SMARTVPN_RUST_SERVICE_STATE='0'
 $env:SMARTVPN_TEST_EXPECT_DOCUMENT_RUST='0'
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestHTTPDispatchIndependent|TestHTTPDispatchService)' .
 if($LASTEXITCODE -ne 0){throw 'Disabled HTTP dispatch regression failed'}
 $env:SMARTVPN_RUST_HTTP_DISPATCH='1'
 $env:SMARTVPN_RUST_SERVICE_STATE='1'
 $env:SMARTVPN_RUST_CORE_DLL=Join-Path $env:TEMP ('missing-http-dispatch-'+[guid]::NewGuid().ToString()+'.dll')
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestHTTPDispatchIndependent|TestHTTPDispatchService)' .
 if($LASTEXITCODE -ne 0){throw 'Missing HTTP dispatch DLL fallback regression failed'}
} finally {Pop-Location}

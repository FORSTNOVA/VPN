param(
 [Parameter(Mandatory=$true)][string]$GoExe,
 [Parameter(Mandatory=$true)][string]$GoPath,
 [string]$Cache=(Join-Path $env:TEMP 'smartvpn-go-parity-cache'),
 [string]$RustTargetDirectory=(Join-Path $PSScriptRoot 'target')
)
$ErrorActionPreference='Stop'
& (Join-Path $PSScriptRoot 'test-profile-offline.ps1') -GoExe $GoExe -GoPath $GoPath -Cache $Cache -RustTargetDirectory $RustTargetDirectory
if($LASTEXITCODE -ne 0){throw 'Prior migration regression suite failed'}
$env:SMARTVPN_RUST_CORE_DLL=Join-Path $RustTargetDirectory 'release\smartvpn_core.dll'
$env:SMARTVPN_RUST_QUOTA_FLOW='1'
Push-Location (Join-Path (Split-Path $PSScriptRoot -Parent) 'backend')
try {
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestQuotaFlow|TestQuotaService|TestMigrationQuota)' .
 if($LASTEXITCODE -ne 0){throw 'Quota refresh offline suite failed'}
 $env:SMARTVPN_RUST_QUOTA_FLOW='0'
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestQuotaService|TestMigrationQuotaHTTP)' .
 if($LASTEXITCODE -ne 0){throw 'Disabled quota refresh regression failed'}
 $env:SMARTVPN_RUST_QUOTA_FLOW='1'
 $env:SMARTVPN_RUST_CORE_DLL=Join-Path $env:TEMP ('missing-quota-flow-'+[guid]::NewGuid().ToString()+'.dll')
 & $GoExe test -tags rustcore -count=1 -timeout=120s -run '^(TestQuotaService|TestMigrationQuotaHTTP)' .
 if($LASTEXITCODE -ne 0){throw 'Missing quota refresh DLL fallback regression failed'}
} finally {Pop-Location}


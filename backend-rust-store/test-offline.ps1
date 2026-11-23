param(
 [Parameter(Mandatory=$true)][string]$GoExe,
 [Parameter(Mandatory=$true)][string]$GoPath,
 [Parameter(Mandatory=$true)][string]$CargoHome,
 [string]$TargetDirectory = (Join-Path $PSScriptRoot 'target'),
 [string]$GoCache = (Join-Path $env:TEMP 'smartvpn-go-parity-cache')
)
$ErrorActionPreference='Stop'
$manifest=Join-Path $PSScriptRoot 'Cargo.toml'
$backend=Join-Path (Split-Path $PSScriptRoot -Parent) 'backend'
$env:CARGO_HOME=$CargoHome
$env:CARGO_TARGET_DIR=$TargetDirectory
$env:CARGO_INCREMENTAL='0'
$env:GOCACHE=$GoCache
$env:GOPATH=$GoPath
$env:GOPROXY='off';$env:GOSUMDB='off';$env:GOTOOLCHAIN='local'
$env:GOFLAGS='-mod=readonly -buildvcs=false';$env:CGO_ENABLED='0'
# Each invocation is a child process. No persistent environment/config changes.
function Run([string]$exe,[string[]]$arguments){ & $exe @arguments; if($LASTEXITCODE){throw "$exe failed ($LASTEXITCODE)"} }
# Avoid ancestor Cargo configuration without editing the user's configuration.
Push-Location ([System.IO.Path]::GetPathRoot($PSScriptRoot))
try {
 Run 'cargo' @('fmt','--manifest-path',$manifest,'--check')
 Run 'cargo' @('test','--manifest-path',$manifest,'--offline','--locked')
 Run 'cargo' @('clippy','--manifest-path',$manifest,'--offline','--locked','--all-targets','--','-D','warnings')
 Run 'cargo' @('build','--manifest-path',$manifest,'--release','--offline','--locked')
} finally {Pop-Location}
$env:SMARTVPN_TEST_STORE_DLL=Join-Path $TargetDirectory 'release\smartvpn_store.dll'
$env:SMARTVPN_RUST_STORE_DLL=''
Push-Location $backend
try {
 # Only dedicated temp-DB/ABI tests, never service or network tests.
 Run $GoExe @('test','-tags','ruststore','-count=1','-timeout=180s','-run','^TestRustStore','.');
} finally {Pop-Location}

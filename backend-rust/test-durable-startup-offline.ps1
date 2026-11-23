param(
 [Parameter(Mandatory=$true)][string]$GoExe,
 [Parameter(Mandatory=$true)][string]$GoPath,
 [Parameter(Mandatory=$true)][string]$Javac,
 [Parameter(Mandatory=$true)][string]$Java,
 [string]$Cache=(Join-Path $env:TEMP 'smartvpn-go-parity-cache'),
 [string]$RustTargetDirectory=(Join-Path $PSScriptRoot 'target')
)
$ErrorActionPreference='Stop'
& (Join-Path $PSScriptRoot 'test-host-http-offline.ps1') -GoExe $GoExe -GoPath $GoPath -Cache $Cache -RustTargetDirectory $RustTargetDirectory
if($LASTEXITCODE -ne 0){throw 'Host/core regression failed'}
$out=Join-Path $env:TEMP ('smartvpn-android-startup-'+[guid]::NewGuid().ToString())
& (Join-Path $PSScriptRoot 'test-android-startup.ps1') -Javac $Javac -Java $Java -OutDir $out
if($LASTEXITCODE -ne 0){throw 'Synthetic Android startup contracts failed'}
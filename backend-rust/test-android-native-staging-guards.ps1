# Offline guard regression. Does not invoke compilers or create directories.
$ErrorActionPreference='Stop'
$root=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$dummy=@{GoExe='unused';GoPath='unused';CargoHome='unused';NdkRoot='unused'}
$cases=@($root,(Join-Path $root 'backend/new-stage'),(Join-Path $root 'backend-rust/new-stage'),(Join-Path $root 'backend-rust-store/new-stage'),(Join-Path $root '.git/new-stage'),(Join-Path $root '.tools/new-stage'),(Join-Path $root 'frontend/android/app/src/main/jniLibs/new-stage'),(Join-Path $root 'new-stage/../backend/new-stage'),(Join-Path $root.ToUpperInvariant() 'BACKEND/new-stage'))
foreach($p in $cases){
  $rejected=$false
  try{& (Join-Path $PSScriptRoot 'package-android-native.ps1') -OutDir $p @dummy}
  catch{if($_.Exception.Message -match 'OutDir must not already exist|Refusing a deployment/source'){$rejected=$true}else{throw}}
  if(-not $rejected){throw "Unsafe output accepted: $p"}
}
Write-Output "Android staging output guards: $($cases.Count) passed"
# Synthetic reparse facts: sandbox does not permit creating junctions.
$mockParent=[IO.Path]::GetDirectoryName($root)
function Get-Item {
  param([string]$LiteralPath,[switch]$Force)
  if($LiteralPath -eq $mockParent){return [pscustomobject]@{Attributes=[IO.FileAttributes]::Directory -bor [IO.FileAttributes]::ReparsePoint}}
  Microsoft.PowerShell.Management\Get-Item -LiteralPath $LiteralPath -Force:$Force
}
try {
  $rejected=$false
  try{& (Join-Path $PSScriptRoot 'package-android-native.ps1') -OutDir (Join-Path $mockParent ('smartvpn-guard-not-created-'+[guid]::NewGuid().ToString('N'))) @dummy}
  catch{if($_.Exception.Message -match 'reparse-point ancestor'){$rejected=$true}else{throw}}
  if(-not $rejected){throw 'Synthetic reparse ancestor guard failed'}
  Write-Output 'Android staging reparse ancestor guard: synthetic fact passed (no real junction tested)'
} finally {Remove-Item Function:Get-Item}

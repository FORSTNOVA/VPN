param(
 [Parameter(Mandatory=$true)][string]$Javac,
 [Parameter(Mandatory=$true)][string]$Java,
 [Parameter(Mandatory=$true)][string]$OutDir
)
$ErrorActionPreference='Stop'
$project=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..')).TrimEnd('\')
$temp=[IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\')
$out=[IO.Path]::GetFullPath($OutDir).TrimEnd('\')
function Within([string]$child,[string]$parent) {
 return $child.Equals($parent,[StringComparison]::OrdinalIgnoreCase) -or $child.StartsWith($parent+'\',[StringComparison]::OrdinalIgnoreCase)
}
if(-not [IO.Path]::IsPathRooted($OutDir) -or -not (Within $out $temp) -or $out -eq $temp -or (Within $out $project)) {
 throw 'OutDir must be a fresh external TEMP child, outside project/source/deployment trees.'
}
if(Test-Path -LiteralPath $out){throw 'OutDir already exists; choose a new external TEMP directory.'}
$ancestor=$out
while($ancestor){
 if(Test-Path -LiteralPath $ancestor){
  if((Get-Item -LiteralPath $ancestor -Force).Attributes -band [IO.FileAttributes]::ReparsePoint){throw 'Reparse-point output ancestor refused.'}
 }
 $next=Split-Path -Parent $ancestor
 if($next -eq $ancestor){break}
 $ancestor=$next
}
foreach($tool in @($Javac,$Java)){
 if(-not [IO.Path]::IsPathRooted($tool) -or -not (Test-Path -LiteralPath $tool -PathType Leaf)){throw 'Supply absolute installed Javac and Java executable paths.'}
}
$helper=Join-Path $project 'frontend/android/app/src/main/java/com/smartvpn/client/RustCoreStartup.java'
$test=Join-Path $PSScriptRoot 'tests/android-startup/RustCoreStartupTest.java'
foreach($inputFile in @($helper,$test)){if(-not(Test-Path -LiteralPath $inputFile -PathType Leaf)){throw "Missing test input: $inputFile"}}
New-Item -ItemType Directory -Path $out | Out-Null
& $Javac -encoding UTF-8 -d $out $helper $test
if($LASTEXITCODE -ne 0){throw "javac failed: $LASTEXITCODE"}
& $Java -cp $out com.smartvpn.client.RustCoreStartupTest
if($LASTEXITCODE -ne 0){throw "java fake contracts failed: $LASTEXITCODE"}
Get-FileHash -Algorithm SHA256 -LiteralPath $helper,$test,$PSCommandPath | Format-Table -AutoSize
Write-Output "PASS: synthetic Java contracts only; classes retained at $out; no JNI, Android, network or VPN execution."

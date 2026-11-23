[CmdletBinding()]
param(
  [Parameter(Mandatory=$true)][string]$OutDir,
  [Parameter(Mandatory=$true)][string]$GoExe,
  [Parameter(Mandatory=$true)][string]$GoPath,
  [Parameter(Mandatory=$true)][string]$CargoHome,
  [Parameter(Mandatory=$true)][string]$NdkRoot,
  [string]$GoCache,
  [ValidateSet('arm64-v8a','x86_64')][string]$Abi='arm64-v8a',
  [switch]$IncludeRustStore,
  [int]$ApiLevel=26
)
$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest
$root=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$out=[IO.Path]::GetFullPath($OutDir)
# A new explicit staging directory only; no deployment, overwrite, or cleanup.
if (Test-Path -LiteralPath $out) { throw 'OutDir must not already exist. Choose a fresh isolated staging directory.' }
if ($out -match '(?i)(jniLibs|[\\/]frontend([\\/]|$))' -or $out.Equals($root,[StringComparison]::OrdinalIgnoreCase) -or $out.StartsWith($root.TrimEnd('\')+'\',[StringComparison]::OrdinalIgnoreCase)) { throw 'Refusing a deployment/source output directory. Choose an external isolated staging directory.' }
# Lexical containment alone is insufficient if an external ancestor is a junction.
# Refuse all existing reparse-point ancestors rather than following them.
$ancestor=[IO.Path]::GetDirectoryName($out)
while($ancestor){
  if(Test-Path -LiteralPath $ancestor){
    $item=Get-Item -LiteralPath $ancestor -Force
    if(($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0){throw 'Refusing output beneath a reparse-point ancestor.'}
  }
  $next=[IO.Path]::GetDirectoryName($ancestor)
  if($next -eq $ancestor){break}
  $ancestor=$next
}
$bin=Join-Path $NdkRoot 'toolchains\llvm\prebuilt\windows-x86_64\bin'
$target=if ($Abi -eq 'x86_64') {'x86_64-linux-android'} else {'aarch64-linux-android'}
$goArch=if ($Abi -eq 'x86_64') {'amd64'} else {'arm64'}
$machine=if ($Abi -eq 'x86_64') {'Advanced Micro Devices X86-64'} else {'AArch64'}
$linkerVariable='CARGO_TARGET_'+$target.ToUpperInvariant().Replace('-','_')+'_LINKER'
$cc=Join-Path $bin "$target$ApiLevel-clang.cmd"
$readelf=Join-Path $bin 'llvm-readelf.exe'
foreach($p in @($GoExe,$cc,$readelf,$CargoHome,$GoPath)) { if(-not(Test-Path -LiteralPath $p)){throw "Missing offline prerequisite: $p"} }
if($ApiLevel -lt 26){throw 'This staging recipe requires API 26 or newer.'}
New-Item -ItemType Directory -Path $out | Out-Null
$envNames=@('CARGO_HOME','CARGO_INCREMENTAL',$linkerVariable,'RUSTFLAGS','GOPATH','GOMODCACHE','GOCACHE','GOPROXY','GOSUMDB','GOTOOLCHAIN','GOWORK','GOFLAGS','GOOS','GOARCH','CGO_ENABLED','CC','CGO_LDFLAGS')
$ccVariable='CC_'+$target.Replace('-','_')
$arVariable='AR_'+$target.Replace('-','_')
$envNames+=@($ccVariable,$arVariable)
$saved=@{}; foreach($n in $envNames){$saved[$n]=[Environment]::GetEnvironmentVariable($n,'Process')}
function Check-Exit([string]$step){if($LASTEXITCODE -ne 0){throw "$step failed with exit code $LASTEXITCODE; staging incomplete."}}
function Verify-ELF([string]$path,[string[]]$symbols){
  $report=(& $readelf --file-header --dynamic --dyn-syms --program-headers --wide $path 2>&1 | Out-String); Check-Exit "readelf $path"
  $report | Set-Content -Encoding UTF8 ($path+'.readelf.txt')
  if($report -notmatch 'Class:\s+ELF64' -or $report -notmatch "Machine:\s+$machine"){throw "Not Android $Abi ELF: $path"}
  foreach($s in $symbols){if($report -notmatch ('GLOBAL\s+DEFAULT\s+\d+\s+'+[regex]::Escape($s)+'(?:\s|$)')){throw "Missing exported $s in $path"}}
  $needed=@([regex]::Matches($report,'\(NEEDED\).*?\[([^\]]+)\]') | ForEach-Object {$_.Groups[1].Value})
  if($path.EndsWith('libsmartvpn.so') -and $needed -contains 'libsmartvpn_core.so'){throw 'Unexpected hard Rust dependency; optional dlopen/rollback contract violated.'}
  if($path.EndsWith('libsmartvpn_core.so') -and $report -notmatch '\(SONAME\).*\[libsmartvpn_core\.so\]'){throw 'Rust SONAME mismatch.'}
  foreach($n in $needed){if($n -notin @('libc.so','libdl.so','libm.so','liblog.so','libandroid.so','libsmartvpn_core.so')){throw "Unexpected unpackaged dependency $n in $path"}}
  foreach($line in ($report -split "`n")){if($line -match '^\s*LOAD\s+.*\s+(0x[0-9a-fA-F]+)\s*$'){if([Convert]::ToInt64($Matches[1].Substring(2),16) -lt 16384){throw "ELF LOAD alignment below 16 KiB: $path"}}}
  return ,$needed
}
function Source-Hashes {
  $files=@(Get-ChildItem (Join-Path $root 'backend') -File | Where-Object {($_.Extension -in @('.go','.c','.h') -and $_.Name -notmatch '_test.go$') -or $_.Name -in @('go.mod','go.sum')})
  $files+=@(Get-ChildItem (Join-Path $PSScriptRoot 'src') -File -Recurse)
  $files+=@(Get-ChildItem (Join-Path $root 'backend/android-tun') -File)
  $files+=@(Get-ChildItem (Join-Path $PSScriptRoot 'host/src') -File -Recurse)
  $files+=@(Get-Item (Join-Path $PSScriptRoot 'host/Cargo.toml'),(Join-Path $PSScriptRoot 'host/Cargo.lock'))
  $files+=@(Get-Item (Join-Path $PSScriptRoot 'Cargo.toml'),(Join-Path $PSScriptRoot 'Cargo.lock'))
  if($IncludeRustStore){
    $files+=@(Get-ChildItem (Join-Path $root 'backend-rust-store/src') -File -Recurse)
    $files+=@(Get-Item (Join-Path $root 'backend-rust-store/Cargo.toml'),(Join-Path $root 'backend-rust-store/Cargo.lock'))
  }
  return (($files | Sort-Object FullName | ForEach-Object { (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash+'  '+$_.FullName.Substring($root.Length+1) }) -join "`n")
}
$sourceBefore=Source-Hashes
try {
  $env:CARGO_HOME=$CargoHome; $env:CARGO_INCREMENTAL='0'; [Environment]::SetEnvironmentVariable($linkerVariable,$cc,'Process')
  $env:RUSTFLAGS='-C link-arg=-Wl,-z,max-page-size=16384 -C link-arg=-Wl,-soname,libsmartvpn_core.so'
  & cargo build --release --offline --locked --manifest-path (Join-Path $PSScriptRoot 'Cargo.toml') --target $target --target-dir (Join-Path $out 'cargo-target') *> (Join-Path $out 'rust-build.log'); Check-Exit 'Rust Android build'
  $native=Join-Path $out "native/$Abi"; $rollback=Join-Path $out "rollback-go/$Abi"
  New-Item -ItemType Directory -Path $native,$rollback | Out-Null
  Copy-Item -LiteralPath (Join-Path $out "cargo-target/$target/release/libsmartvpn_core.so") -Destination $native
  $env:RUSTFLAGS='-C link-arg=-Wl,-z,max-page-size=16384'
  & cargo rustc --lib --crate-type cdylib --release --offline --locked --manifest-path (Join-Path $PSScriptRoot 'host/Cargo.toml') --target $target --target-dir (Join-Path $out 'cargo-target') -- -C link-arg=-Wl,-soname,libsmartvpn_host.so *> (Join-Path $out 'host-build.log'); Check-Exit 'Rust Android host build'
  Copy-Item -LiteralPath (Join-Path $out "cargo-target/$target/release/libsmartvpn_host.so") -Destination $native
  if($IncludeRustStore){
    [Environment]::SetEnvironmentVariable($ccVariable,$cc,'Process')
    [Environment]::SetEnvironmentVariable($arVariable,(Join-Path $bin 'llvm-ar.exe'),'Process')
    $env:RUSTFLAGS='-C link-arg=-Wl,-z,max-page-size=16384 -C link-arg=-Wl,-soname,libsmartvpn_store.so'
    & cargo build --release --offline --locked --manifest-path (Join-Path $root 'backend-rust-store/Cargo.toml') --target $target --target-dir (Join-Path $out 'cargo-target') *> (Join-Path $out 'store-build.log'); Check-Exit 'Rust Android SQLite build'
    Copy-Item -LiteralPath (Join-Path $out "cargo-target/$target/release/libsmartvpn_store.so") -Destination $native
  }
  $env:GOPATH=$GoPath; $env:GOMODCACHE=Join-Path $GoPath 'pkg\mod'; $env:GOCACHE=if($GoCache){[IO.Path]::GetFullPath($GoCache)}else{Join-Path $out 'go-cache'}
  $env:GOPROXY='off'; $env:GOSUMDB='off'; $env:GOTOOLCHAIN='local'; $env:GOWORK='off'; $env:GOFLAGS='-mod=readonly -buildvcs=false'
  $env:GOOS='android'; $env:GOARCH=$goArch; $env:CGO_ENABLED='1'; $env:CC=$cc; $env:CGO_LDFLAGS=''
  Push-Location (Join-Path $root 'backend')
  try {
    $overlay = Join-Path $out 'android-tun-overlay.json'
    & "$root/backend/android-tun/overlay.ps1" -GoExe $GoExe -OutFile $overlay
    & $GoExe build "-overlay=$overlay" -tags 'cmfa,with_gvisor,rustcore' -buildmode=c-shared '-ldflags=-s -w -extldflags=-Wl,-z,max-page-size=16384' -o (Join-Path $native 'libsmartvpn.so') . *> (Join-Path $out 'go-native-build.log'); Check-Exit 'Go rustcore Android build'
    & $GoExe build "-overlay=$overlay" -tags 'cmfa,with_gvisor' -buildmode=c-shared '-ldflags=-s -w -extldflags=-Wl,-z,max-page-size=16384' -o (Join-Path $rollback 'libsmartvpn.so') . *> (Join-Path $out 'go-rollback-build.log'); Check-Exit 'Go rollback Android build'
  } finally {Pop-Location}
  $jni=@('SmartVPNRustCoreConfigure','SmartVPNRustCoreConfigureN','JNI_OnLoad','Java_com_smartvpn_client_CoreBridge_start','Java_com_smartvpn_client_CoreBridge_stop','Java_com_smartvpn_client_CoreBridge_setTunnel')
  # Verify every literal Rust ABI name referenced by current non-test Go code,
  # not only the headline version exports (version gates also remain required).
  $rustSymbols=@(Get-ChildItem (Join-Path $root 'backend') -Filter '*.go' | Where-Object {$_.Name -notmatch '_test.go$'} | Select-String -CaseSensitive -AllMatches -Pattern '"(smartvpn_[a-zA-Z0-9_]+_v[0-9]+)"' | ForEach-Object {$_.Matches | ForEach-Object {$_.Groups[1].Value}} | Where-Object {$_ -ne 'smartvpn_windows_kernel_version_v1' -and $_ -notlike 'smartvpn_store_*' -and $_ -notlike 'smartvpn_host_*'} | Sort-Object -Unique)
  if($rustSymbols.Count -lt 10){throw 'Rust ABI symbol inventory unexpectedly empty/incomplete.'}
  $rustSymbols | Set-Content -Encoding UTF8 (Join-Path $out 'required-rust-symbols.txt')
  $artifacts=@()
  foreach($p in @((Join-Path $native 'libsmartvpn_host.so'),(Join-Path $native 'libsmartvpn_core.so'),(Join-Path $native 'libsmartvpn.so'),(Join-Path $rollback 'libsmartvpn.so'))){
    $symbols=if($p.EndsWith('libsmartvpn_core.so')){$rustSymbols}elseif($p.EndsWith('libsmartvpn_host.so')){@('smartvpn_host_start_v1','smartvpn_host_stop_v1')}else{$jni}
    $deps=Verify-ELF $p $symbols
    $artifacts+= [ordered]@{path=$p.Substring($out.Length+1);sha256=(Get-FileHash -LiteralPath $p -Algorithm SHA256).Hash;bytes=(Get-Item -LiteralPath $p).Length;needed=$deps;requiredExports=$symbols}
  }
  if($IncludeRustStore){
    $p=Join-Path $native 'libsmartvpn_store.so'
    $symbols=@('version','capabilities','open','claim','call','close','result_size','result_read','result_free') | ForEach-Object {"smartvpn_store_${_}_v1"}
    $deps=Verify-ELF $p $symbols
    $artifacts+= [ordered]@{path=$p.Substring($out.Length+1);sha256=(Get-FileHash -LiteralPath $p -Algorithm SHA256).Hash;bytes=(Get-Item -LiteralPath $p).Length;needed=$deps;requiredExports=$symbols}
  }
  $sourceAfter=Source-Hashes
  if($sourceBefore -cne $sourceAfter){throw 'Build inputs changed during staging; choose a fresh OutDir after sources freeze.'}
  $sourceBefore | Set-Content -Encoding UTF8 (Join-Path $out 'sources.sha256')
  [ordered]@{schema=1;abi=$Abi;api=$ApiLevel;sourceRoot=$root;rust=(& rustc --version | Out-String).Trim();go=(& $GoExe version | Out-String).Trim();ndk=$NdkRoot;tags='cmfa,with_gvisor,rustcore';runtimeValidated=$false;artifacts=$artifacts} | ConvertTo-Json -Depth 6 | Set-Content -Encoding UTF8 (Join-Path $out 'manifest.json')
  Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'ANDROID_NATIVE_STAGING.md') -Destination (Join-Path $out 'PACKAGING.md')
  Write-Output "Validated offline staging: $out (Android loading/runtime NOT tested)"
} finally {foreach($n in $envNames){[Environment]::SetEnvironmentVariable($n,$saved[$n],'Process')}}


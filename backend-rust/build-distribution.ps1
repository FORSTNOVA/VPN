param(
  [Parameter(Mandatory)][string]$Destination,
  [ValidateSet('windows','x86_64','arm64-v8a')][string]$Platform='windows',
  [string]$Compiler
)
$ErrorActionPreference='Stop'
$root=Split-Path $PSScriptRoot -Parent
$targetDir=Join-Path $root '.tools/distribution-cargo'
$android=$Platform -ne 'windows'
$options=@('--locked','--release','--config','source.crates-io.replace-with="ustc"','--config','source.ustc.registry="sparse+https://mirrors.ustc.edu.cn/crates.io-index/"')
$output=Join-Path $targetDir 'release'
$save=@{}
function ProcessEnv($name,$value) {
  if(-not $save.ContainsKey($name)){ $save[$name]=[Environment]::GetEnvironmentVariable($name,'Process') }
  [Environment]::SetEnvironmentVariable($name,$value,'Process')
}
try {
  if(-not $android){
    # cc-rs must discover MSVC even when the caller just built an Android APK.
    ProcessEnv 'CC' $null
    $options+=@('--target','x86_64-pc-windows-msvc')
    $output=Join-Path $targetDir 'x86_64-pc-windows-msvc/release'
  }
  if($android){
    $target=if($Platform -eq 'x86_64'){'x86_64-linux-android'}else{'aarch64-linux-android'}
    $key=$target.Replace('-','_')
    ProcessEnv ('CARGO_TARGET_'+$key.ToUpperInvariant()+'_LINKER') $Compiler
    ProcessEnv "CC_$key" $Compiler
    ProcessEnv "AR_$key" (Join-Path (Split-Path $Compiler) 'llvm-ar.exe')
    $options+=@('--target',$target)
    $output=Join-Path $targetDir "$target/release"
  }
  New-Item -ItemType Directory -Force $Destination | Out-Null
  foreach($lib in 'core','host','store','engine'){
    $manifest=switch($lib){'core'{"$PSScriptRoot/Cargo.toml"} 'host'{"$PSScriptRoot/host/Cargo.toml"} 'store'{"$root/backend-rust-store/Cargo.toml"} 'engine'{"$PSScriptRoot/engine/Cargo.toml"}}
    $file=if($android){"libsmartvpn_$lib.so"}elseif($lib -eq 'host'){'smartvpn-host.exe'}else{"smartvpn_$lib.dll"}
    # The engine is both a host dependency and a standalone JNI library. Its
    # independent lockfile/linker flags must not overwrite the host's rlib.
    $libTarget=if($lib -eq 'engine'){Join-Path $targetDir 'standalone-engine'}else{$targetDir}
    $libOutput=Join-Path $libTarget ($output.Substring($targetDir.Length).TrimStart('\','/'))
    $libOptions=$options+@('--target-dir',$libTarget)
    $cargoCache=if($env:CARGO_HOME){$env:CARGO_HOME}else{Join-Path $env:USERPROFILE '.cargo'}
    $flags=@("--remap-path-prefix=$root=/src","--remap-path-prefix=$env:USERPROFILE=/home/builder","--remap-path-prefix=$cargoCache=/cargo")
    if($android){$flags+=@('-C','link-arg=-Wl,-z,max-page-size=16384')}
    ProcessEnv 'CARGO_ENCODED_RUSTFLAGS' ($flags -join [char]31)
    if($android -and $lib -eq 'host'){
      & cargo rustc --lib --crate-type cdylib @libOptions --manifest-path $manifest -- -C "link-arg=-Wl,-soname,$file"
    }elseif($android){
      & cargo rustc --lib @libOptions --manifest-path $manifest -- -C "link-arg=-Wl,-soname,$file"
    }else{
      & cargo build @libOptions --manifest-path $manifest
    }
    if($LASTEXITCODE){throw "Rust $lib build failed for $Platform"}
    Copy-Item -LiteralPath (Join-Path $libOutput $file) -Destination $Destination -Force
  }
} finally {foreach($name in $save.Keys){[Environment]::SetEnvironmentVariable($name,$save[$name],'Process')}}

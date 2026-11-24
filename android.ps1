<#
.SYNOPSIS
  Builds the Android app: the kernel library first, then the package around it.

.DESCRIPTION
  The Android half is two builds that have to happen in this order, because the
  application contains the service rather than starting it. The Go library —
  kernel, scheduling and local API — is compiled for the device's architecture
  and placed among the app's native libraries, and only then is the Flutter
  application built and packaged around it.

  The library is the reason this is not "flutter build apk" alone: nothing in
  the Flutter build knows how to produce it, and a package built without it
  installs and then fails at the first call.

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File android.ps1
  powershell -ExecutionPolicy Bypass -File android.ps1 -Abi arm64-v8a,x86_64
#>
[CmdletBinding()]
param(
  [string[]]$Abi = @('arm64-v8a'),
  [string]$NdkVersion = '27.2.12479018',
  [string]$ApiLevel = '26',
  [ValidateSet('Rust','Go')][string]$BackendMode='Rust',
  [switch]$NoPub,
  [switch]$Release
)

$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$backend = Join-Path $root 'backend'
$frontend = Join-Path $root 'frontend'
$tools = Join-Path $root '.tools'
$jniLibs = Join-Path $tools "distribution-native/android/$BackendMode"
$Abi = @($Abi | ForEach-Object { $_ -split ',' } | Select-Object -Unique)
$tags = 'cmfa,with_gvisor'
if ($BackendMode -eq 'Rust') { $tags += ',rustcore' }

$sdk = if ($env:ANDROID_HOME) { $env:ANDROID_HOME } else { Join-Path $env:LOCALAPPDATA 'Android\Sdk' }
$ndkBin = Join-Path $sdk "ndk\$NdkVersion\toolchains\llvm\prebuilt\windows-x86_64\bin"
if (-not (Test-Path $ndkBin)) { throw "no NDK at $ndkBin; pass -NdkVersion for one that is installed" }

$env:GOCACHE = Join-Path $tools 'go-cache'
$env:GOPATH = Join-Path $tools 'gopath'
$env:GOMODCACHE = Join-Path $tools 'gopath\pkg\mod'
$env:GOPROXY = 'https://goproxy.cn,direct'
$env:GOTOOLCHAIN = 'local'
$env:GOOS = 'android'
$env:CGO_ENABLED = '1'

$supportedAbis = @('arm64-v8a', 'x86_64')
foreach ($requestedAbi in $Abi) {
  if ($requestedAbi -cnotin $supportedAbis) { throw "unknown ABI $requestedAbi (expected $($supportedAbis -join ', '))" }
}

if (Test-Path -LiteralPath $jniLibs) {
  $nativeRoot = (Resolve-Path -LiteralPath $jniLibs).Path
  foreach ($dir in Get-ChildItem -LiteralPath $nativeRoot -Directory -Force) {
    if ($dir.Name -in ($supportedAbis + @('armeabi-v7a', 'x86')) -and $dir.Name -notin $Abi) {
      if ($dir.Parent.FullName -ne $nativeRoot -or ($dir.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
        throw "Unsafe ABI cleanup path: $($dir.FullName)"
      }
      Remove-Item -LiteralPath $dir.FullName -Recurse -Force
    }
  }
}

foreach ($oneAbi in $Abi) {
  # The variable is not called $abi on purpose: PowerShell gives a loop variable
  # the type of the collection it walks, and the parameter $Abi is [string[]], so
  # $abi would hold the whole array on every iteration.
  $arch = ''
  $clang = ''
  switch ($oneAbi) {
    'arm64-v8a' { $arch = 'arm64'; $clang = 'aarch64-linux-android' }
    'x86_64'    { $arch = 'amd64'; $clang = 'x86_64-linux-android' }
  }

  $out = Join-Path $jniLibs $oneAbi
  New-Item -ItemType Directory -Path $out -Force | Out-Null
  $env:GOARCH = $arch
  $env:CC = Join-Path $ndkBin ($clang + $ApiLevel + '-clang.cmd')

  if ($BackendMode -eq 'Rust') {
    & "$root/backend-rust/build-distribution.ps1" -Destination $out -Platform $oneAbi -Compiler $env:CC
    Remove-Item -LiteralPath (Join-Path $out 'libsmartvpn.so') -Force -ErrorAction SilentlyContinue
  } else {
    Write-Host "== service library for $oneAbi =="
    Push-Location $backend
    $overlay = New-TemporaryFile
    try {
      & "$backend/android-tun/overlay.ps1" -GoExe "$tools/go/bin/go.exe" -OutFile $overlay.FullName
      & (Join-Path $tools 'go\bin\go.exe') build "-overlay=$($overlay.FullName)" -tags $tags -buildmode=c-shared -ldflags='-s -w -extldflags=-Wl,-z,max-page-size=16384' `
        -o (Join-Path $out 'libsmartvpn.so') .
      if ($LASTEXITCODE -ne 0) { throw "the library build failed for $oneAbi" }
    } finally {
      Remove-Item -LiteralPath $overlay.FullName
      Pop-Location
    }
    $size = [math]::Round((Get-Item (Join-Path $out 'libsmartvpn.so')).Length / 1MB, 1)
    Write-Host "   libsmartvpn.so $size MB"
  }
}

Write-Host '== package =='
Push-Location $frontend
try {
  $mode = if ($Release) { '--release' } else { '--debug' }
  $targetPlatforms = ($Abi | ForEach-Object {
    switch ($_) {
      'arm64-v8a' { 'android-arm64' }
      'x86_64' { 'android-x64' }
    }
  }) -join ','
  $options=@("--android-project-arg=smartvpnNativeDir=$jniLibs",
             "--android-project-arg=smartvpnAndroidAbi=$($Abi -join ',')",
             "--android-project-arg=smartvpnRustCoreEnabled=$(($BackendMode -eq 'Rust').ToString().ToLowerInvariant())",
             "--android-project-arg=disable-abi-filtering=true")
  if($NoPub){$options+='--no-pub'}
  & (Join-Path $tools 'flutter/bin/flutter.bat') build apk $mode "--target-platform=$targetPlatforms" @options
  if ($LASTEXITCODE -ne 0) { throw 'the Flutter build failed' }

} finally {
  Pop-Location
}

$variant=if($Release){'release'}else{'debug'}
$apk=Get-Item (Join-Path $frontend "build/app/outputs/flutter-apk/app-$variant.apk")
& python "$root/backend-rust/verify_distribution_apk.py" $apk.FullName $BackendMode ($Abi -join ',')
if($LASTEXITCODE){throw 'APK backend/ABI verification failed'}
$version=(Select-String -Path "$frontend/pubspec.yaml" -Pattern '^version:\s*([\w.-]+)').Matches[0].Groups[1].Value
$out=Join-Path $root 'dist/android'
New-Item -ItemType Directory -Path $out -Force | Out-Null
$suffix=if($BackendMode -eq 'Go'){'-go-rollback'}else{''}
$dest=Join-Path $out "SmartVPN-$version-$variant-$($Abi -join '_')$suffix.apk"
Copy-Item -LiteralPath $apk.FullName -Destination $dest -Force
Write-Output "package: $dest"

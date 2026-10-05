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
  [switch]$Release
)

$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$backend = Join-Path $root 'backend'
$frontend = Join-Path $root 'frontend'
$tools = Join-Path $root '.tools'
$jniLibs = Join-Path $frontend 'android\app\src\main\jniLibs'

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

foreach ($oneAbi in $Abi) {
  # The variable is not called $abi on purpose: PowerShell gives a loop variable
  # the type of the collection it walks, and the parameter $Abi is [string[]], so
  # $abi would hold the whole array on every iteration.
  $arch = ''
  $clang = ''
  switch ($oneAbi) {
    'arm64-v8a' { $arch = 'arm64'; $clang = 'aarch64-linux-android' }
    'x86_64'    { $arch = 'amd64'; $clang = 'x86_64-linux-android' }
    default     { throw "unknown ABI $oneAbi (arm64-v8a and x86_64 are the known ones)" }
  }

  $out = Join-Path $jniLibs $oneAbi
  New-Item -ItemType Directory -Path $out -Force | Out-Null
  $env:GOARCH = $arch
  $env:CC = Join-Path $ndkBin ($clang + $ApiLevel + '-clang.cmd')

  Write-Host "== service library for $oneAbi =="
  Push-Location $backend
  try {
    # Stripped for the same reason the Windows service is, and because this one
    # is inside a package someone downloads.
    #
    # The build tags are not optional.
    #
    # cmfa: without it the kernel builds its own Android routing rules when its
    # TUN starts, which means reading the system's package list — a file no
    # ordinary app may open. The listener then fails, and nothing works. With
    # the tag, the kernel leaves routing to the VpnService that made the tunnel,
    # which is where it belongs.
    #
    # with_gvisor: the tunnel's stack in the generated configuration is gvisor,
    # and the cmfa tag drops it, so the kernel refuses to start the listener
    # with a message saying exactly that.
    & (Join-Path $tools 'go\bin\go.exe') build -tags cmfa,with_gvisor -buildmode=c-shared -ldflags='-s -w' `
      -o (Join-Path $out 'libsmartvpn.so') .
    if ($LASTEXITCODE -ne 0) { throw "the library build failed for $oneAbi" }
  } finally {
    Pop-Location
  }
  $size = [math]::Round((Get-Item (Join-Path $out 'libsmartvpn.so')).Length / 1MB, 1)
  Write-Host "   libsmartvpn.so $size MB"
}

Write-Host '== package =='
Push-Location $frontend
try {
  $mode = if ($Release) { '--release' } else { '--debug' }
  & (Join-Path $tools 'flutter\bin\flutter.bat') build apk $mode
  if ($LASTEXITCODE -ne 0) { throw 'the Flutter build failed' }
} finally {
  Pop-Location
}

$apk = Get-ChildItem (Join-Path $frontend 'build\app\outputs\flutter-apk') -Filter '*.apk' |
  Sort-Object LastWriteTime -Descending | Select-Object -First 1
$size = [math]::Round($apk.Length / 1MB, 1)
Write-Host ''
Write-Host "package: $($apk.FullName) ($size MB, $($Abi -join ', '))"
Write-Host 'install: adb install -r <the apk above>'

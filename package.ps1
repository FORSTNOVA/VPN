<#
.SYNOPSIS
  Assembles the portable bundle: one zip the recipient can unzip and run.

.DESCRIPTION
  Builds both halves first, then stages the window, the local service, the helper
  the Windows proxy needs, the Mihomo kernel and wintun.dll, plus the licence
  material and a short start-here note. The kernel and the library are taken from
  the local installation, which is where a working copy lives; without the kernel
  the package cannot connect at all, so that is an error unless
  -AllowMissingKernel says otherwise.

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File package.ps1
#>
[CmdletBinding()]
param(
  [string]$OutputDir = '',
  [switch]$SkipBuild,
  [switch]$AllowMissingKernel
)

$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$frontend = Join-Path $root 'frontend'
$backend = Join-Path $root 'backend'
$tools = Join-Path $root '.tools'
$release = Join-Path $frontend 'build\windows\x64\runner\Release'
$dataDir = Join-Path $env:APPDATA 'SmartVPN'

$version = '0.0.0'
$match = Select-String -Path (Join-Path $frontend 'pubspec.yaml') -Pattern '^version:\s*([0-9]+\.[0-9]+\.[0-9]+)' |
  Select-Object -First 1
if ($match) { $version = $match.Matches.Groups[1].Value }

if (-not $SkipBuild) {
  Write-Host '== service =='
  $env:GOCACHE = Join-Path $tools 'go-cache'
  $env:GOPATH = Join-Path $tools 'gopath'
  $env:GOPROXY = 'https://goproxy.cn,direct'
  $env:GOTOOLCHAIN = 'local'
  # Stripped on purpose: an unstripped build of this size is quarantined by
  # Huorong as an obfuscator (see the README).
  Push-Location $backend
  try {
    & (Join-Path $tools 'go\bin\go.exe') build -ldflags='-s -w' `
      -o (Join-Path $release 'smartvpn-service-current.exe') .
    if ($LASTEXITCODE -ne 0) { throw 'the service build failed' }
  } finally {
    Pop-Location
  }

  Write-Host '== window =='
  Push-Location $frontend
  try {
    & (Join-Path $tools 'flutter\bin\flutter.bat') build windows --release
    if ($LASTEXITCODE -ne 0) { throw 'the Flutter build failed' }
  } finally {
    Pop-Location
  }
}

$stage = Join-Path $env:TEMP ('smartvpn-package\' + "SmartVPN-$version")
if (Test-Path $stage) { Remove-Item $stage -Recurse -Force }
New-Item -ItemType Directory -Path $stage | Out-Null

Write-Host '== staging =='
# The window, the Flutter runtime and the data folder it reads at startup.
foreach ($name in 'smartvpn_windows.exe', 'flutter_windows.dll', 'data', 'native_assets.json') {
  Copy-Item (Join-Path $release $name) $stage -Recurse
}
# The local service, and the helper it shells out to for the Windows proxy.
foreach ($name in 'smartvpn-service-current.exe', 'proxy.ps1') {
  Copy-Item (Join-Path $release $name) $stage
}

$missing = @()
foreach ($name in 'mihomo.exe', 'wintun.dll') {
  $source = Join-Path $dataDir $name
  if (Test-Path $source) {
    Copy-Item $source $stage
  } else {
    $missing += $name
  }
}
if ($missing.Count -gt 0) {
  $message = "not found in $dataDir : $($missing -join ', ')"
  if ($AllowMissingKernel) {
    Write-Warning "$message - the package will not work until the recipient supplies them"
  } else {
    throw "$message - a package without them cannot connect; put them there first, or pass -AllowMissingKernel"
  }
}

# What the package redistributes, and where its source lives.
$kernelVersion = ''
if (Test-Path (Join-Path $dataDir 'mihomo.exe')) {
  $reported = & (Join-Path $dataDir 'mihomo.exe') -v 2>&1 | Select-Object -First 1
  if ($reported) { $kernelVersion = [string]$reported }
}
$licenses = Join-Path $stage 'LICENSES'
New-Item -ItemType Directory -Path $licenses | Out-Null
Copy-Item (Join-Path $root 'licenses\GPL-3.0.txt') $licenses
Copy-Item (Join-Path $root 'licenses\使用说明.txt') $stage

# The marker that makes this copy self-contained: with it beside the executable,
# everything the service writes — settings, node cache, logs, the database, the
# subscription URL — stays in the data folder in this same directory.
@(
  '便携模式标记文件（portable.txt）',
  '',
  '这个文件的存在表示：本目录是便携版，程序的数据（设置、节点缓存、日志、数据库、',
  '订阅地址）都放在本文件夹的 SmartVPN-data 里，不写进 %APPDATA%。',
  '因此整个文件夹可以随意移动、复制、删除。',
  '',
  '删掉这个文件，程序就会改回把数据放在 %APPDATA%\SmartVPN。'
) | Set-Content -Path (Join-Path $stage 'portable.txt') -Encoding UTF8

@(
  'This package contains the following third-party components.',
  '',
  'Mihomo (the proxy kernel, mihomo.exe)',
  '  Licence: GNU General Public License v3 - the full text is in LICENSES/GPL-3.0.txt',
  '  Source:  https://github.com/MetaCubeX/mihomo',
  $(if ($kernelVersion) { "  Version: $kernelVersion" } else { '  Version: unknown (mihomo.exe could not be queried)' }),
  '  Redistributed unmodified; the source of this exact version is at the address above.',
  '',
  'wintun (the TUN driver library, wintun.dll)',
  '  Provided by WireGuard LLC and redistributed unmodified under the terms it',
  '  publishes: https://www.wintun.net/',
  '',
  'Flutter and Dart (the application framework: smartvpn_windows.exe,',
  'flutter_windows.dll and data\)',
  '  Licence: BSD-style, plus the licence of every package they bundle. The',
  '  complete notices ship with the application and are the ones it shows on its',
  '  own licence page: data\flutter_assets\NOTICES.Z, compressed.',
  '  Source: https://github.com/flutter/flutter and https://dart.dev',
  '',
  'SmartVPN itself is not covered by these licences.'
) | Set-Content -Path (Join-Path $licenses 'NOTICE.txt') -Encoding ASCII

if ($OutputDir -eq '') { $OutputDir = Join-Path $root 'dist' }
New-Item -ItemType Directory -Path $OutputDir -Force | Out-Null
$zip = Join-Path $OutputDir "SmartVPN-$version-portable.zip"
if (Test-Path $zip) { Remove-Item $zip -Force }

Write-Host '== compressing =='
Compress-Archive -Path $stage -DestinationPath $zip -CompressionLevel Optimal -Force
Remove-Item $stage -Recurse -Force

$size = [math]::Round((Get-Item $zip).Length / 1MB, 1)
Write-Host ''
Write-Host "package: $zip ($size MB)"
Write-Host 'contents: smartvpn_windows.exe, flutter_windows.dll, data\, native_assets.json,'
Write-Host '          smartvpn-service-current.exe,'
Write-Host '          proxy.ps1, mihomo.exe, wintun.dll, portable.txt, 使用说明.txt, LICENSES\'

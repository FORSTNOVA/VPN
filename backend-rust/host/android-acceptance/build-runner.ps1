[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$AndroidSdk,
    [Parameter(Mandatory)][string]$Jdk,
    [Parameter(Mandatory)][string]$TestKeyStore,
    [Parameter(Mandatory)][string]$OutDir
)
$ErrorActionPreference = 'Stop'
if (-not $env:SMARTVPN_TEST_STOREPASS) { throw 'Set SMARTVPN_TEST_STOREPASS for the APK test key.' }
$key = (Resolve-Path -LiteralPath $TestKeyStore).Path
$out = (New-Item -ItemType Directory -Path $OutDir).FullName
$classes = (New-Item -ItemType Directory -Path (Join-Path $out 'classes')).FullName
$bt = Join-Path $AndroidSdk 'build-tools/36.0.0'
$platform = Join-Path $AndroidSdk 'platforms/android-36/android.jar'
$javaHomeBefore = $env:JAVA_HOME
function RequireSuccess($step) { if ($LASTEXITCODE -ne 0) { throw "$step failed ($LASTEXITCODE)" } }
Push-Location -LiteralPath $out
try {
    $env:JAVA_HOME = $Jdk
    $sources = @(Get-ChildItem -LiteralPath $PSScriptRoot -Filter '*.java' | ForEach-Object FullName)
    & "$Jdk/bin/javac.exe" --release 8 -cp $platform -d $classes $sources
    RequireSuccess 'javac'
    & "$Jdk/bin/jar.exe" cf runner.jar -C $classes .
    RequireSuccess 'jar'
    & "$bt/d8.bat" --min-api 26 --lib $platform --output $out runner.jar
    RequireSuccess 'd8'
    & "$bt/aapt2.exe" link -o base.apk --manifest "$PSScriptRoot/AndroidManifest.xml" -I $platform
    RequireSuccess 'aapt2'
    & "$bt/aapt.exe" add base.apk classes.dex
    RequireSuccess 'add dex'
    & "$bt/zipalign.exe" 4 base.apk aligned.apk
    RequireSuccess 'zipalign'
    & "$bt/apksigner.bat" sign --ks $key --ks-pass env:SMARTVPN_TEST_STOREPASS --out runner.apk aligned.apk
    RequireSuccess 'sign test APK'
    & "$bt/apksigner.bat" verify runner.apk
    RequireSuccess 'verify test APK'
} finally {
    Pop-Location
    $env:JAVA_HOME = $javaHomeBefore
}

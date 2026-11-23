[CmdletBinding()]
param(
 [Parameter(Mandatory=$true)][string]$BuildRoot,
 [Parameter(Mandatory=$true)][string]$OriginalFrontend,
 [Parameter(Mandatory=$true)][string]$NativeStage,
 [Parameter(Mandatory=$true)][string]$FlutterSdk,
 [Parameter(Mandatory=$true)][string]$GradleExe,
 [Parameter(Mandatory=$true)][string]$GradleDependencyCache,
 [Parameter(Mandatory=$true)][string]$PubCache,
 [Parameter(Mandatory=$true)][string]$AndroidSdk,
 [Parameter(Mandatory=$true)][string]$JavaHome
)
$ErrorActionPreference='Stop'
$stage=[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$root=[IO.Path]::GetFullPath($BuildRoot)
if(Test-Path -LiteralPath $root){throw 'BuildRoot must be new; no resume/overwrite'}
$inputs=@($stage,$OriginalFrontend,(Split-Path ([IO.Path]::GetFullPath($OriginalFrontend)) -Parent),$NativeStage,$FlutterSdk,$GradleDependencyCache,$PubCache,$AndroidSdk,$JavaHome,(Split-Path (Split-Path ([IO.Path]::GetFullPath($GradleExe)) -Parent) -Parent))
function AssertNoReparse($path){
 $p=[IO.Path]::GetFullPath($path)
 while($p){if((Test-Path -LiteralPath $p)-and ((Get-Item -LiteralPath $p -Force).Attributes-band[IO.FileAttributes]::ReparsePoint)){throw "Reparse path: $p"};$p=[IO.Path]::GetDirectoryName($p)}
}
foreach($inputPath in $inputs){
 $src=[IO.Path]::GetFullPath($inputPath).TrimEnd('\')
 AssertNoReparse $src
 if($root.Equals($src,[StringComparison]::OrdinalIgnoreCase)-or $root.StartsWith($src+'\',[StringComparison]::OrdinalIgnoreCase)){throw 'BuildRoot must be outside every input directory'}
}
AssertNoReparse $root
# Never follow links while copying either SDK or dependency cache.
foreach($tree in @($FlutterSdk,$GradleDependencyCache)){
 if(Get-ChildItem -LiteralPath $tree -Recurse -Force -Attributes ReparsePoint -ErrorAction Stop | Select-Object -First 1){throw "Reparse entry in copied input: $tree"}
}
function AssertExit($step){if($LASTEXITCODE -ne 0){throw "$step failed: $LASTEXITCODE; inspect isolated logs"}}
function CopyTree($src,$dst,$exclude){
 & robocopy $src $dst /E /XJ /R:0 /W:0 /NFL /NDL /NJH /NJS /NP /XD .git .dart_tool .idea build /XF lockfile @exclude | Out-Null
 if($LASTEXITCODE-ge 8){throw "Copy failed: $src"}
}
New-Item -ItemType Directory -Path $root | Out-Null
$names=@('JAVA_HOME','PATH','GRADLE_USER_HOME','ANDROID_HOME','ANDROID_SDK_ROOT','ANDROID_USER_HOME','PUB_CACHE','FLUTTER_ROOT','CI','FLUTTER_SUPPRESS_ANALYTICS','DART_SUPPRESS_ANALYTICS','FLUTTER_TOOL_ARGS')
$saved=@{};foreach($n in $names){$saved[$n]=[Environment]::GetEnvironmentVariable($n,'Process')}
try {
 & python (Join-Path $PSScriptRoot 'android_apk_staging.py') copy $OriginalFrontend (Join-Path $stage 'frontend') (Join-Path $root 'frontend') (Join-Path $root 'frontend-source-hashes.json');AssertExit 'source copy'
 CopyTree $FlutterSdk (Join-Path $root 'flutter-sdk') @()
 # Git metadata is only the SDK version database, not application repository data.
 & robocopy (Join-Path $FlutterSdk '.git') (Join-Path $root 'flutter-sdk\.git') /E /XJ /R:0 /W:0 /NFL /NDL /NJH /NJS /NP /XD hooks /XF config '*.lock' | Out-Null
 if($LASTEXITCODE-ge 8){throw 'SDK version metadata copy failed'}
 CopyTree $GradleDependencyCache (Join-Path $root 'gradle-home\caches\modules-2') @('gc.properties','*.lock')
 $env:JAVA_HOME=$JavaHome;$env:PATH=(Join-Path $JavaHome 'bin')+';'+$env:PATH
 $env:GRADLE_USER_HOME=Join-Path $root 'gradle-home';$env:ANDROID_USER_HOME=Join-Path $root 'android-home'
 $env:ANDROID_HOME=$AndroidSdk;$env:ANDROID_SDK_ROOT=$AndroidSdk;$env:PUB_CACHE=$PubCache
 $env:FLUTTER_ROOT=Join-Path $root 'flutter-sdk';$env:CI='true';$env:FLUTTER_SUPPRESS_ANALYTICS='true';$env:DART_SUPPRESS_ANALYTICS='true'
 $env:FLUTTER_TOOL_ARGS=''
 # Refuse any SDK launcher state which could rebuild tools (and invoke pub).
 $sdk=$env:FLUTTER_ROOT;$cache=Join-Path $sdk 'bin\cache';$tools=Join-Path $sdk 'packages\flutter_tools'
 if(Test-Path (Join-Path $sdk 'bin\internal\bootstrap.bat')){throw 'Unreviewed SDK bootstrap hook'}
 foreach($f in @('flutter_tools.snapshot','flutter_tools.stamp','engine.stamp','engine-dart-sdk.stamp')){if(-not(Test-Path (Join-Path $cache $f))){throw "Missing cached SDK file $f"}}
 if((Get-Content (Join-Path $cache 'engine.stamp') -Raw).Trim() -cne (Get-Content (Join-Path $cache 'engine-dart-sdk.stamp') -Raw).Trim()){throw 'SDK update would be required'}
 if(-not(Test-Path (Join-Path $tools 'pubspec.lock')) -or (Get-Item (Join-Path $tools 'pubspec.yaml')).LastWriteTimeUtc -gt (Get-Item (Join-Path $tools 'pubspec.lock')).LastWriteTimeUtc){throw 'Tool dependency update would be required'}
 $launcher=Get-Content (Join-Path $sdk 'bin\internal\shared.bat') -Raw
 # Preserve the installed SDK launcher behavior; do not patch it or its stamp.
 $revision=if($launcher.Contains('$git rev-parse HEAD')){''}else{(& git -C $sdk rev-parse HEAD | Out-String).Trim()}
 if((Get-Content (Join-Path $cache 'flutter_tools.stamp') -Raw).Trim() -cne ('"'+$revision+':"')){throw 'Tool snapshot rebuild would be required; no pub permitted'}
 $assemble=Get-Content (Join-Path $tools 'lib\src\commands\assemble.dart') -Raw
 if($assemble -match 'usesPubOption\('){throw 'Unexpected assemble dependency-resolution option'}
 $nm=Get-Content (Join-Path $NativeStage 'manifest.json') -Raw | ConvertFrom-Json
 $abi=$nm.abi
 if($abi -notin @('arm64-v8a','x86_64')){throw "Unsupported native stage ABI: $abi"}
 $platform=if($abi -eq 'x86_64'){'android-x64'}else{'android-arm64'}
 foreach($artifact in $nm.artifacts){if((Get-FileHash (Join-Path $NativeStage $artifact.path)).Hash -cne $artifact.sha256){throw 'Native manifest hash mismatch'}}
 foreach($line in (Get-Content (Join-Path $NativeStage 'sources.sha256'))){if($line){if((Get-FileHash (Join-Path $stage $line.Substring(66))).Hash -cne $line.Substring(0,64)){throw 'Native source is stale; rebuild isolated native stage'}}}
 $front=Join-Path $root 'frontend';$android=Join-Path $front 'android'
 @("sdk.dir=$($AndroidSdk.Replace('\','/'))","flutter.sdk=$($env:FLUTTER_ROOT.Replace('\','/'))",'flutter.buildMode=release','flutter.versionName=0.2.19','flutter.versionCode=32') | Set-Content -Encoding ASCII (Join-Path $android 'local.properties')
 @('org.gradle.jvmargs=-Xmx4G -XX:MaxMetaspaceSize=2G','android.useAndroidX=true','android.newDsl=false','android.builtInKotlin=false','org.gradle.daemon=false') | Set-Content -Encoding ASCII (Join-Path $android 'gradle.properties')
 $key=Join-Path $root 'debug.keystore'
 & (Join-Path $JavaHome 'bin\keytool.exe') -genkeypair -keystore $key -storepass android -keypass android -alias androiddebugkey -keyalg RSA -keysize 2048 -validity 30 -dname 'CN=Isolated Offline Debug,O=Local Test,C=US' *> (Join-Path $root 'debug-key.log');AssertExit 'isolated debug key'
 & (Join-Path $JavaHome 'bin\keytool.exe') -exportcert -alias androiddebugkey -keystore $key -storepass android -file (Join-Path $root 'debug-cert.der') *> (Join-Path $root 'debug-cert.log');AssertExit 'temporary certificate export'
 $certificateHash=(Get-FileHash (Join-Path $root 'debug-cert.der')).Hash.ToLowerInvariant()
 & python (Join-Path $PSScriptRoot 'android_apk_staging.py') patch-gradle $OriginalFrontend (Join-Path $stage 'frontend') $front (Join-Path $root 'frontend-source-hashes.json') $key;AssertExit 'exact staging patch'
 # No pub command: reuse only the already resolved local graph after validation.
 & python (Join-Path $PSScriptRoot 'relocate_android_package_config.py') $OriginalFrontend $front $FlutterSdk $env:FLUTTER_ROOT $PubCache *> (Join-Path $root 'local-package-config.log');AssertExit 'local package graph relocation'
 $jni=Join-Path $android "app/src/main/jniLibs/$abi";New-Item -ItemType Directory $jni -Force | Out-Null
 $reports=@()
 foreach($mode in @('go','rust')){
  $native=if($mode-eq 'go'){Join-Path $NativeStage "rollback-go/$abi"}else{Join-Path $NativeStage "native/$abi"}
  Copy-Item -LiteralPath (Join-Path $native 'libsmartvpn.so') -Destination $jni
  if($mode-eq 'rust'){Copy-Item -LiteralPath (Join-Path $native 'libsmartvpn_core.so'),(Join-Path $native 'libsmartvpn_host.so') -Destination $jni}
  if($mode -eq 'rust' -and (Test-Path -LiteralPath (Join-Path $native 'libsmartvpn_store.so'))){
    $storeEntries=@($nm.artifacts | Where-Object {($_.path -replace '\\','/') -ceq "native/$abi/libsmartvpn_store.so"})
    if($storeEntries.Count -ne 1){throw 'Rust store missing from verified native manifest'}
    Copy-Item -LiteralPath (Join-Path $native 'libsmartvpn_store.so') -Destination $jni
  }
  Push-Location $android
  $opt=@('-PsmartvpnRustCoreEnabled=false');if($mode-eq 'rust'){$opt=@('-PsmartvpnRustCoreEnabled=true')}
  try {& $GradleExe --offline --no-daemon --max-workers=2 "-Ptarget-platform=$platform" "-PsmartvpnAndroidAbi=$abi" "-Pdisable-abi-filtering=true" @opt :app:assembleRelease *> (Join-Path $root "gradle-$mode.log");AssertExit "offline Gradle $mode"}finally{Pop-Location}
  $apk=Get-ChildItem (Join-Path $front 'build') -Recurse -Filter app-release.apk | Select-Object -First 1
  if(-not $apk){throw 'assemble returned without APK'}
  $dest=Join-Path $root "smartvpn-$mode-test-signed.apk";Copy-Item $apk.FullName $dest
  & python (Join-Path $PSScriptRoot 'android_apk_staging.py') inspect $dest $native $mode (Join-Path $root "apk-$mode.json");AssertExit 'APK inventory/native hash'
  $bt=Join-Path $AndroidSdk 'build-tools\36.0.0'
  & (Join-Path $bt 'apksigner.bat') verify --verbose --print-certs $dest *> (Join-Path $root "signature-$mode.txt");AssertExit 'APK signature'
  $signature=Get-Content (Join-Path $root "signature-$mode.txt") -Raw
  $certs=[regex]::Matches($signature,'Signer #\d+ certificate SHA-256 digest: ([0-9a-fA-F]+)')
  if($certs.Count -ne 1 -or $certs[0].Groups[1].Value.ToLowerInvariant() -cne $certificateHash){throw 'APK not signed by this isolated run certificate'}
  & (Join-Path $bt 'zipalign.exe') -c -P 16 -v 4 $dest *> (Join-Path $root "alignment-$mode.txt");AssertExit 'APK zip alignment'
  $badging=(& (Join-Path $bt 'aapt.exe') dump badging $dest | Out-String);AssertExit 'APK manifest decode'
  $badging | Set-Content -Encoding UTF8 (Join-Path $root "manifest-$mode.txt")
  if($badging -notmatch "package: name='com.smartvpn.client'" -or $badging -notmatch "sdkVersion:'26'" -or $badging -notmatch "native-code: '$abi'"){throw 'APK manifest/ABI mismatch'}
  $config=Get-ChildItem (Join-Path $front 'build\app\generated') -Recurse -Filter BuildConfig.java | Where-Object {$_.FullName -match 'release'} | Select-Object -First 1
  $expected=($mode-eq 'rust').ToString().ToLowerInvariant()
  if(-not $config -or (Get-Content $config.FullName -Raw) -notmatch "SMARTVPN_RUST_CORE_ENABLED = $expected;"){throw 'BuildConfig opt-in mismatch'}
  Copy-Item $config.FullName (Join-Path $root "BuildConfig-$mode.java")
  $reports+=Get-Content (Join-Path $root "apk-$mode.json") -Raw | ConvertFrom-Json
 }
 & python (Join-Path $PSScriptRoot 'android_apk_staging.py') audit-source $OriginalFrontend (Join-Path $stage 'frontend') $front (Join-Path $root 'frontend-source-hashes.json') $key;AssertExit 'frontend source audit'
 [ordered]@{apkValidated=$true;signerCertificateSha256=$certificateHash;runtimeValidated=$false;variant='release-unminified-temporary-test-key';dependencyResolutionPerformed=$false;nativeManifestSha256=(Get-FileHash (Join-Path $NativeStage 'manifest.json')).Hash;frontendManifestSha256=(Get-FileHash (Join-Path $root 'frontend-source-hashes.json')).Hash;artifacts=$reports} | ConvertTo-Json -Depth 6 | Set-Content -Encoding UTF8 (Join-Path $root 'apk-success-manifest.json')
 Write-Output "Offline APK artifacts built at $root. No installation/runtime validation."
} finally {foreach($n in $names){[Environment]::SetEnvironmentVariable($n,$saved[$n],'Process')}}




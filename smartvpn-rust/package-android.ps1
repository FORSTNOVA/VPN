param([ValidateSet('arm64-v8a','x86_64')][string]$Abi = 'arm64-v8a')

# Flutter and Gradle use an isolated home. Restore the caller's process environment
# so a subsequent ABI build discovers the real Android SDK.
$originalEnvironment = [Environment]::GetEnvironmentVariables('Process')
$project = $null
try {
    . "$PSScriptRoot/build-env.ps1"
    & "$PSScriptRoot/build-android.ps1" -Abi $Abi
    $repo = Split-Path $PSScriptRoot
    $work = Join-Path $env:TEMP 'phase4b-tools'
    . "$PSScriptRoot/stage-client.ps1"
    # Flutter, its included Gradle build, pub and the wrapper all write local caches.
    Copy-Tree "$repo/.tools/flutter" "$work/flutter"
    $env:GRADLE_USER_HOME="$work/gradle"
    Copy-Tree "$env:USERPROFILE/.gradle/caches/modules-2" "$work/gradle/caches/modules-2"
    Copy-Tree "$env:USERPROFILE/.gradle/wrapper/dists/gradle-9.3.1-all" "$work/gradle/wrapper/dists/gradle-9.3.1-all"
    Copy-Tree "$env:LOCALAPPDATA/Pub/Cache" "$work/pub"
    $env:JAVA_HOME="$env:ProgramFiles/Android/Android Studio/jbr"
    $env:ANDROID_HOME="$env:LOCALAPPDATA/Android/sdk"
    $env:ANDROID_USER_HOME="$work/android-home"
    New-Item -ItemType Directory -Force $env:ANDROID_USER_HOME | Out-Null
    Copy-Item "$env:USERPROFILE/.android/debug.keystore" "$env:ANDROID_USER_HOME/debug.keystore" -Force
    $env:PUB_CACHE="$work/pub"
    $env:FLUTTER_SUPPRESS_ANALYTICS='true'
    $env:CI='true'
    $env:APPDATA="$work/appdata"
    $env:LOCALAPPDATA="$work/localappdata"
    $env:GRADLE_OPTS="-Duser.home=`"$work`" -Dorg.gradle.daemon=false"
    $project=New-Item -ItemType Directory "$work/project-$([guid]::NewGuid().ToString('N').Substring(0,8))"
    $platform = if ($Abi -eq 'x86_64') { 'android-x64' } else { 'android-arm64' }
    $native = New-Item -ItemType Directory "$project/native/$Abi" -Force
    Copy-Item "$PSScriptRoot/dist/android/$Abi/libsmartvpn.so" $native.FullName
    foreach ($folder in 'assets','lib','android'){
        Copy-Tree "$repo/frontend/$folder" "$project/$folder" @('/XD','.gradle','.kotlin','.idea','build','jniLibs')
    }
    $licenses=New-Item -ItemType Directory -Force "$project/android/app/src/main/assets/licenses"
    Copy-Item "$PSScriptRoot/vendor/clash-rs/LICENSE" "$licenses/Apache-2.0-clash-rs.txt"
    Copy-Item "$repo/frontend/pubspec.yaml","$repo/frontend/pubspec.lock","$repo/frontend/.metadata" $project
    $version=(Get-Content "$PSScriptRoot/VERSION").Split('+')
    Set-DirectClient $project.FullName
    Set-RustAndroidVpnSettings $project.FullName
    Push-Location $project.FullName
    try {
        & "$work/flutter/bin/flutter.bat" pub get --offline
        Check-Exit 'flutter pub get'
        & "$work/flutter/bin/flutter.bat" build apk --release --no-pub "--target-platform=$platform" "--build-name=$($version[0])" "--build-number=$($version[1])" `
            "--android-project-arg=smartvpnNativeDir=$project/native" --android-project-arg=smartvpnAndroidAbi=$Abi `
            --android-project-arg=smartvpnRustCoreEnabled=false --android-project-arg=disable-abi-filtering=true
        Check-Exit 'Flutter release APK'
    } finally { Pop-Location }
    $apk="$project/build/app/outputs/flutter-apk/app-release.apk"
    & python -I -B "$PSScriptRoot/tests/verify_pure_packages.py" $apk --abi $Abi
    Check-Exit 'APK verification'
    Copy-Item $apk "$PSScriptRoot/dist/SmartVPN-$($version[0])-rust-pure-$Abi.apk" -Force
} finally {
    foreach ($entry in [Environment]::GetEnvironmentVariables('Process').GetEnumerator()) {
        if ($originalEnvironment[$entry.Key] -cne $entry.Value) {
            [Environment]::SetEnvironmentVariable($entry.Key, $originalEnvironment[$entry.Key], 'Process')
        }
    }
    if ($null -ne $project) {
        $createdPath = (Resolve-Path -LiteralPath $project.FullName).Path
        $workPath = (Resolve-Path -LiteralPath $work).Path
        if ((Split-Path -Parent $createdPath) -ne $workPath -or (Split-Path -Leaf $createdPath) -notmatch '^project-[0-9a-f]{8}$') {
            throw 'Unexpected Android staging path'
        }
        Remove-Item -LiteralPath $createdPath -Recurse -Force
    }
}

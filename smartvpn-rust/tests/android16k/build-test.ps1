param([Parameter(Mandatory)][string]$Work)
$ErrorActionPreference='Stop'
$env:JAVA_HOME="$env:ProgramFiles/Android/Android Studio/jbr"
$bin="$env:JAVA_HOME/bin"
$sdk="$env:LOCALAPPDATA/Android/sdk"
$android="$sdk/platforms/android-36/android.jar"
$build="$sdk/build-tools/36.0.0"
function Verify([string]$step){if($LASTEXITCODE){throw "$step failed ($LASTEXITCODE)"}}
New-Item -ItemType Directory -Force "$Work/classes" | Out-Null
& "$bin/javac" --release 11 -classpath $android -d "$Work/classes" "$PSScriptRoot/Acceptance.java" "$PSScriptRoot/TunnelProbe.java"
Verify javac
& "$build/d8.bat" --lib $android --min-api 33 --output $Work "$Work/classes/com/smartvpn/acceptance/Acceptance.class" "$Work/classes/com/smartvpn/acceptance/TunnelProbe.class"
Verify d8
& "$build/aapt2" link -I $android --manifest "$PSScriptRoot/AndroidManifest.xml" -o "$Work/test.apk"
Verify aapt2
& "$bin/jar" uf "$Work/test.apk" -C $Work classes.dex
Verify jar
& "$build/apksigner.bat" sign --ks "$env:USERPROFILE/.android/debug.keystore" --ks-pass pass:android "$Work/test.apk"
Verify apksigner

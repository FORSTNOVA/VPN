. "$PSScriptRoot/build-env.ps1"
& "$PSScriptRoot/build-windows.ps1"
$repo=Split-Path $PSScriptRoot
. "$PSScriptRoot/stage-client.ps1"
$work = Join-Path $env:TEMP 'phase4c-windows-tools'
Copy-Tree "$repo/.tools/flutter" "$work/flutter"
Copy-Tree "$env:LOCALAPPDATA/Pub/Cache" "$work/pub"
$env:PUB_CACHE="$work/pub"
$env:FLUTTER_SUPPRESS_ANALYTICS='true'
$env:CI='true'
$env:APPDATA="$work/appdata"
$env:LOCALAPPDATA="$work/localappdata"
# MSBuild FileTracker cannot create nested tracking logs under long paths.
$project=New-Item -ItemType Directory "$env:CARGO_TARGET_DIR/win-$([guid]::NewGuid().ToString('N').Substring(0,8))"
foreach ($folder in 'assets','lib','windows'){
    Copy-Tree "$repo/frontend/$folder" "$project/$folder" @('/XD','ephemeral')
}
Copy-Item "$repo/frontend/pubspec.yaml","$repo/frontend/pubspec.lock","$repo/frontend/.metadata" $project
$buildVersion=(Get-Content "$PSScriptRoot/VERSION").Trim()
$manifest="$project/pubspec.yaml"
$spec=[IO.File]::ReadAllText($manifest)
if ([regex]::Matches($spec,'(?m)^version: [^\r\n]+').Count -ne 1) { throw 'Expected one Flutter package version' }
[IO.File]::WriteAllText($manifest,[regex]::Replace($spec,'(?m)^version: [^\r\n]+',"version: $buildVersion"))
$env:DOTNET_CLI_HOME=$work
$env:MSBUILDDISABLENODEREUSE='1'
Set-DirectClient $project.FullName
Push-Location $project.FullName
try {
    & "$work/flutter/bin/flutter.bat" pub get --offline
    Check-Exit 'flutter pub get'
    & "$work/flutter/bin/flutter.bat" build windows --release --no-pub
    Check-Exit 'Flutter Windows release bundle'
} finally { Pop-Location }
$release="$project/build/windows/x64/runner/Release"
if ((Get-Item "$release/smartvpn_windows.exe").VersionInfo.ProductVersion -ne $buildVersion) { throw 'Windows runner product version differs from VERSION' }
$stage=New-Item -ItemType Directory "$env:TEMP/windows-package-$([guid]::NewGuid())"
foreach ($name in 'smartvpn_windows.exe','flutter_windows.dll','data'){
    Copy-Item -LiteralPath "$release/$name" -Destination $stage -Recurse
}
Copy-Item "$PSScriptRoot/dist/windows-x64/smartvpn-rust-service.exe" "$stage/smartvpn-service-current.exe"
Copy-Item -LiteralPath "$repo/.tools/gopath/pkg/mod/github.com/metacubex/sing-tun@v0.4.24/internal/wintun/amd64/wintun.dll" -Destination "$stage/wintun.dll"
New-Item -ItemType Directory "$stage/LICENSES" | Out-Null
Copy-Item "$PSScriptRoot/vendor/clash-rs/LICENSE" "$stage/LICENSES/Apache-2.0-clash-rs.txt"
New-Item -ItemType File "$stage/portable.txt" | Out-Null
Set-Content "$stage/README-rust-pure.txt" 'Extract the entire ZIP and run smartvpn_windows.exe. Local settings are stored in SmartVPN-data/smartvpn.db. The service listens on loopback and starts its embedded clash-rs kernel on connect. Run the client as administrator to use Windows TUN mode with the bundled x64 wintun.dll.'
$version=(Get-Content "$PSScriptRoot/VERSION").Split('+')[0]
Add-Type -AssemblyName System.IO.Compression.FileSystem
$zip="$($stage.FullName).zip"
[IO.Compression.ZipFile]::CreateFromDirectory($stage.FullName,$zip)
& python -I -B "$PSScriptRoot/tests/verify_pure_packages.py" $zip
Check-Exit 'ZIP verification'
Copy-Item $zip "$PSScriptRoot/dist/SmartVPN-$version-rust-pure-windows-x64.zip" -Force

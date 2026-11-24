. "$PSScriptRoot/build-env.ps1"
cargo build --target-dir "$env:CARGO_TARGET_DIR" --locked --manifest-path "$PSScriptRoot/Cargo.toml" -p smartvpn-win-service --target x86_64-pc-windows-msvc --release
Check-Exit 'Windows release'
$dist = New-Item -Force -ItemType Directory "$PSScriptRoot/dist/windows-x64"
$exe = "$env:CARGO_TARGET_DIR/x86_64-pc-windows-msvc/release/smartvpn-rust-service.exe"
Copy-Item -LiteralPath $exe -Destination $dist.FullName -Force
(Get-Item $exe).Length

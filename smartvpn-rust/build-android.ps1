param([ValidateSet('arm64-v8a','x86_64')][string]$Abi = 'arm64-v8a')
. "$PSScriptRoot/build-env.ps1"
$bin="$env:LOCALAPPDATA\Android\sdk\ndk\27.2.12479018\toolchains\llvm\prebuilt\windows-x86_64\bin"
$target = if ($Abi -eq 'x86_64') { 'x86_64-linux-android' } else { 'aarch64-linux-android' }
$key = $target.Replace('-', '_')
$cargoKey = $key.ToUpperInvariant()
Set-Item "env:CARGO_TARGET_${cargoKey}_LINKER" "$bin/${target}26-clang.cmd"
Set-Item "env:CC_$key" "$bin/${target}26-clang.cmd"
Set-Item "env:CXX_$key" "$bin/${target}26-clang++.cmd"
Set-Item "env:AR_$key" "$bin/llvm-ar.exe"
Set-Item "env:CARGO_TARGET_${cargoKey}_RUSTFLAGS" '--cfg tokio_unstable -C link-arg=-Wl,-z,max-page-size=16384 -C link-arg=-Wl,--wrap=socket'
$env:RUSTC_BOOTSTRAP = '1'
cargo build --target-dir "$env:CARGO_TARGET_DIR" --locked --manifest-path "$PSScriptRoot/Cargo.toml" -p smartvpn-android-jni --target $target --release
Check-Exit 'Android release'
$library = "$env:CARGO_TARGET_DIR/$target/release/libsmartvpn.so"
python -I -B "$PSScriptRoot/tests/verify_elf.py" $library "$bin/llvm-readelf.exe" --abi $Abi
Check-Exit 'ELF verification'
$dist = New-Item -Force -ItemType Directory "$PSScriptRoot/dist/android/$Abi"
Copy-Item -LiteralPath $library -Destination $dist.FullName -Force


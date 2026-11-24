function Copy-Tree($source,$dest,[string[]]$exclude=@()) {
    robocopy $source $dest /E /R:0 /W:0 /MT:12 /NFL /NDL /NJH /NJS /NP /XF *.lock go-sqlite3.txt @exclude | Out-Null
    if ($LASTEXITCODE -ge 8) { throw "robocopy failed for $source (exit $LASTEXITCODE)" }
}

function Set-DirectClient([string]$projectDir) {
    $file = Join-Path $projectDir 'lib/main.dart'
    $source = [IO.File]::ReadAllText($file)
    $pattern = 'final client = HttpClient()..connectionTimeout'
    if ([regex]::Matches($source, [regex]::Escape($pattern)).Count -ne 1) {
        throw "Expected one Flutter loopback HTTP client in staged main.dart"
    }
    # Parentheses terminate the cascade assignment before the timeout setter.
    $direct = "final client = HttpClient()..findProxy = ((_) => 'DIRECT')..connectionTimeout"
    $source = $source.Replace($pattern,$direct)
    $anchor = [regex]'TextButton\(\s+onPressed: \(\) => edit\(entry\.key\),'
    $match = $anchor.Matches($source)
    if ($match.Count -ne 1) { throw 'Expected chain edit action' }
    $delete = [IO.File]::ReadAllText("$PSScriptRoot/client/chain-delete.dart.inc")
    [IO.File]::WriteAllText($file, $source.Insert($source.IndexOf($match[0].Value), $delete))
}
function Set-RustAndroidVpnSettings([string]$projectDir) {
    $file = Join-Path $projectDir 'android/app/src/main/java/com/smartvpn/client/SmartVpnService.java'
    $source = [IO.File]::ReadAllText($file)
    $start = $source.IndexOf('        java.io.File settings = ')
    $end = $source.IndexOf('        ParcelFileDescriptor established = builder.establish();')
    if ($start -lt 0 -or $end -le $start) { throw 'Expected Android VPN builder settings block' }
    $block = [IO.File]::ReadAllText("$PSScriptRoot/client/android-game-bypass.java.inc")
    [IO.File]::WriteAllText($file, $source.Substring(0, $start) + $block + $source.Substring($end))
    $source = [IO.File]::ReadAllText($file)
    $watch = '    private void watchNetworks() {'
    $register = '        NetworkRequest tunnelRequest ='
    $unregister = '        if (manager == null || vpnCallback == null)'
    foreach ($marker in $watch,$register,$unregister) {
        if ([regex]::Matches($source,[regex]::Escape($marker)).Count -ne 1) { throw 'Unexpected Android network callback layout' }
    }
    $network = [IO.File]::ReadAllText("$PSScriptRoot/client/android-network.java.inc")
    $source = $source.Replace($watch,$network + $watch).Replace($register,"        watchUplink(manager);`n$register").Replace($unregister,"        unwatchUplink(manager);`n$unregister")
    [IO.File]::WriteAllText($file,$source)
    $bridge = Join-Path $projectDir 'android/app/src/main/java/com/smartvpn/client/CoreBridge.java'
    $source = [IO.File]::ReadAllText($bridge)
    $anchor = '    public static boolean protect('
    if ([regex]::Matches($source,[regex]::Escape($anchor)).Count -ne 1) { throw 'Expected native bridge socket callbacks' }
    $owner = [IO.File]::ReadAllText("$PSScriptRoot/client/android-owner.java.inc")
    [IO.File]::WriteAllText($bridge, $source.Replace($anchor,$owner + $anchor))

}

[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$Adb,
    [Parameter(Mandatory)][string]$Serial,
    [Parameter(Mandatory)][string]$OutDir,
    [ValidateRange(1,100)][int]$Runs = 1,
    [switch]$RequireReadHost,
    [switch]$RequireWriteHost,
    [switch]$TestLiveMerge
)
$ErrorActionPreference = 'Stop'
function Device {
    $lines = & $Adb -s $Serial @args
    if ($LASTEXITCODE -ne 0) { throw "adb failed on $Serial ($LASTEXITCODE)" }
    $lines
}
if ((Device shell echo healthy) -ne 'healthy') { throw 'Device unavailable' }
$package = Device shell pm list packages -U com.smartvpn.client
if ($package -notmatch '^package:com\.smartvpn\.client uid:(\d+)$') { throw 'Missing client UID' }
$clientUid = $Matches[1]
$vpn = Device shell dumpsys vpn_management
if (-not ($vpn -match 'Active vpn type:\s*-1\s*$') -or ($vpn -match 'Active vpn type:\s*\d+')) {
    throw 'This series needs an idle Android VPN'
}
$out = (New-Item -ItemType Directory -Path $OutDir).FullName
Device shell dumpsys package com.smartvpn.client | Select-String 'primaryCpuAbi=' |
    Set-Content (Join-Path $out 'installed-abi.txt')
$apk = @((Device shell pm path com.smartvpn.client) -replace '^package:', '')
Device shell sha256sum @apk | Set-Content (Join-Path $out 'installed-hashes.txt')
$failures = 0
for ($i = 1; $i -le $Runs; $i++) {
    $start = Device shell date '+%s.%N'
    if ($start -notmatch '^\d+\.\d+$') { throw 'Missing device timestamp' }
    $instrumentArgs = if ($RequireWriteHost) { @('-e','writeHost','1') } elseif ($RequireReadHost) { @('-e', 'readHost', '1') } else { @() }
    if ($TestLiveMerge) { $instrumentArgs = @($instrumentArgs) + @('-e','liveMerge','1') }
    $result = @(& $Adb -s $Serial shell timeout -k 5 240 am instrument -w -r @instrumentArgs com.smartvpn.acceptance/.DeviceRun)
    $instrumentExit = $LASTEXITCODE
    $result | Set-Content (Join-Path $out "run-$i.txt")
    # Use device epoch time: the host and Android clocks can differ.
    $faults = @(Device logcat -b all -v epoch -d -T $start "--uid=$clientUid" 'Go:E' 'libc:F' 'AndroidRuntime:E' 'houdini:I' '*:S')
    $faults | Set-Content (Join-Path $out "runtime-$i.log")
    $ok = ($instrumentExit -eq 0) -and ($result -contains 'INSTRUMENTATION_CODE: -1') -and
        -not ($faults -match 'mspan\.sweep: bad span state|fatal error:|panic: runtime error:|Fatal signal|FATAL EXCEPTION|\[SIGABRT\]')
    if (-not $ok) {
        $failures++
        Device shell dumpsys activity exit-info com.smartvpn.client |
            Set-Content (Join-Path $out "exit-$i.txt")
        if ($instrumentExit -ne 0) {
            throw "Instrumentation command exited with $instrumentExit; inspect run $i before starting another session"
        }
    }
    Write-Output "Run $i/$Runs passed=$ok"
}
if ($failures) { throw "$failures/$Runs runs failed; see $out" }

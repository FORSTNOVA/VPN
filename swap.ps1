<#
.SYNOPSIS
  Puts a staged rebuild of the Windows half into place and brings it up.

.DESCRIPTION
  On this machine the window and the local service both run with an
  administrator token, because TUN needs one. Only an elevated shell can stop
  them, and only an elevated launcher can start the window with the token the
  tunnel needs. So this script has to be run as administrator: that is the one
  manual step, and everything after it is automatic.

  It stops the running pair through their own API rather than killing them
  first, so the route table, the DNS setting and the system proxy are put back
  the way they were. Only then does it take the files, and only after backing
  the current ones up. If the new build does not come up it puts the old one
  back and reconnects, so a failed swap does not leave the machine offline.

  The one thing to expect: the VPN is down for about a minute, because the
  kernel has to stop and start again for the new service binary to be the one
  running.

.PARAMETER Stage
  Where the rebuild was staged. Expects bin\smartvpn-service-current.exe and
  win\ (a copy of the Flutter release folder). Defaults to .stage beside this
  script.

.PARAMETER Release
  The folder to install into. Defaults to the Flutter build output.

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File swap.ps1
#>
[CmdletBinding()]
param(
  [string]$Stage = '',
  [string]$Release = '',
  [switch]$SkipConnect
)

$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $MyInvocation.MyCommand.Path
if ($Stage -eq '') { $Stage = Join-Path $root '.stage' }
if ($Release -eq '') { $Release = Join-Path $root 'frontend\build\windows\x64\runner\Release' }
$bin = Join-Path $Stage 'bin'
$win = Join-Path $Stage 'win'
$backup = Join-Path $Stage 'backup'
$log = Join-Path $Stage 'swap.log'
$record = Join-Path (Join-Path $env:APPDATA 'SmartVPN') 'service.json'

# The names this script replaces. proxy.ps1 is here because the system proxy
# needs it and the Flutter build does not produce it: it comes from the source
# tree, and leaving the old copy behind would pair a new service with an old
# helper. native_assets.json is left alone for the same reason in reverse —
# this build does not emit one, and the manifest in place is the empty one.
$names = @('smartvpn-service-current.exe', 'smartvpn_windows.exe',
           'flutter_windows.dll', 'proxy.ps1', 'data')

function Say([string]$message) {
  $line = '{0} {1}' -f (Get-Date -Format 'HH:mm:ss'), $message
  Write-Host $line
  try { Add-Content -Path $log -Value $line -Encoding ASCII } catch { }
}

# The last thing the script does is hold the window open: run through
# Start-Process, this console is all the user sees of what happened, and it
# would otherwise close on top of the summary.
function Finish([int]$code, [string]$summary) {
  Say $summary
  Write-Host ''
  Write-Host 'The full account of this run is in .stage\swap.log'
  $null = Read-Host 'Press Enter to close this window'
  exit $code
}

function Get-Record {
  if (-not (Test-Path $record)) { return $null }
  try { return Get-Content -Raw -Path $record | ConvertFrom-Json } catch { return $null }
}

function Invoke-Service([string]$method, [string]$route, [int]$timeoutSec) {
  $r = Get-Record
  if ($null -eq $r) { throw 'the service is not running' }
  $headers = @{ Authorization = 'Bearer ' + $r.token }
  $url = 'http://127.0.0.1:{0}{1}' -f $r.port, $route
  return Invoke-RestMethod -Method $method -Uri $url -Headers $headers -TimeoutSec $timeoutSec
}

function Wait-For([scriptblock]$test, [int]$seconds) {
  $deadline = (Get-Date).AddSeconds($seconds)
  while ($true) {
    try { if (& $test) { return $true } } catch { }
    if ((Get-Date) -ge $deadline) { return $false }
    Start-Sleep -Milliseconds 500
  }
}

function Stop-Everything {
  foreach ($name in 'smartvpn_windows', 'smartvpn-service-current', 'mihomo') {
    Get-Process -Name $name -ErrorAction SilentlyContinue | ForEach-Object {
      Say ('force-stopping {0} (pid {1})' -f $_.ProcessName, $_.Id)
      Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue
    }
  }
  Start-Sleep -Milliseconds 800
}

# Replacing a file a process still holds open fails with a sharing violation.
# The processes are gone by now, but Windows takes a moment to let go.
function Move-Aside([string]$path, [string]$dest) {
  $deadline = (Get-Date).AddSeconds(30)
  while ($true) {
    try {
      Move-Item -Path $path -Destination $dest -Force -ErrorAction Stop
      return
    } catch {
      if ((Get-Date) -ge $deadline) { throw }
      Start-Sleep -Milliseconds 500
    }
  }
}

function Start-App {
  Say 'starting the window'
  # Through the shell's "run as administrator" verb rather than directly. A
  # directly started child inherits this console, and a console control event
  # reaches every process attached to it, so closing this window after the swap
  # would take the new window down with it. The verb goes through the elevation
  # service instead, which has no console of its own, so the window comes up
  # detached — and since this script is already elevated, no second prompt
  # appears.
  Start-Process -FilePath (Join-Path $Release 'smartvpn_windows.exe') -Verb RunAs | Out-Null
  $started = Wait-For {
    $r = Get-Record
    return ($null -ne $r) -and ($r.pid -ne $script:oldPid)
  } 90
  if (-not $started) { return $false }
  $r = Get-Record
  $script:newPid = $r.pid
  Say ('the new service is up on port {0} (pid {1})' -f $r.port, $r.pid)
  # Worth stating rather than assuming: TUN needs an administrator token, and a
  # launch that quietly lost one would leave the app unable to tunnel.
  try {
    $state = Invoke-Service 'GET' '/api/state' 10
    if (-not $state.elevated) {
      Say 'WARNING: the new service is not elevated, so TUN will not be available'
    }
  } catch { }
  return $true
}

function Connect-Tunnel {
  Say 'connecting'
  try { Invoke-Service 'POST' '/api/connect' 180 | Out-Null }
  catch { Say ('connect failed: ' + $_.Exception.Message); return $false }
  $up = Wait-For { return (Invoke-Service 'GET' '/api/state' 10).connected } 120
  if (-not $up) { Say 'the service never reported a connection'; return $false }
  $state = Invoke-Service 'GET' '/api/state' 10
  Say ('connected, tunActive={0}, node={1}' -f $state.tunActive, $state.activeNode)
  return $true
}

function Restore-Previous {
  Say 'restoring the previous build'
  Stop-Everything
  foreach ($name in $names) {
    $saved = Join-Path $backup $name
    $dest = Join-Path $Release $name
    if (Test-Path $saved) {
      # The half-copied new build is in the way, and Move-Item onto an existing
      # directory would put the old one inside it instead of replacing it.
      if (Test-Path $dest) { Remove-Item $dest -Recurse -Force -ErrorAction SilentlyContinue }
      Move-Aside $saved $dest
    }
  }
  $script:oldPid = 0
  if (-not (Start-App)) { Say 'the previous build did not come up either'; return }
  if (-not $SkipConnect) { $null = Connect-Tunnel }
}

# ---------------------------------------------------------------------------

if (-not ([Security.Principal.WindowsPrincipal] `
      [Security.Principal.WindowsIdentity]::GetCurrent()
    ).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
  Write-Host 'This script has to run as administrator: the window and the' -ForegroundColor Red
  Write-Host 'service it replaces are running with an administrator token.' -ForegroundColor Red
  exit 1
}

if (Test-Path $log) { Remove-Item $log -Force }
Say 'swap starting'
Say ('staged service: ' + (Join-Path $bin 'smartvpn-service-current.exe'))
Say ('staged window:  ' + $win)
Say ('installing into: ' + $Release)

if (-not (Test-Path (Join-Path $bin 'smartvpn-service-current.exe'))) {
  Finish 1 'RESULT ok=false reason=no-staged-service'
}
if (-not (Test-Path (Join-Path $win 'smartvpn_windows.exe'))) {
  Finish 1 'RESULT ok=false reason=no-staged-window'
}

$oldPid = 0
$newPid = 0
$running = Get-Record
if ($null -ne $running) { $oldPid = $running.pid }
Say ('the running service is pid {0}' -f $oldPid)

Say 'the VPN goes down here and comes back in about a minute'
try { Invoke-Service 'POST' '/api/disconnect' 20 | Out-Null; Say 'asked the service to disconnect' }
catch { Say ('disconnect was not accepted: ' + $_.Exception.Message) }
$null = Wait-For { return -not (Invoke-Service 'GET' '/api/state' 5).connected } 40
try { Invoke-Service 'POST' '/api/shutdown' 10 | Out-Null; Say 'asked the service to stop' }
catch { Say ('shutdown was not accepted: ' + $_.Exception.Message) }
$null = Wait-For { return $null -eq (Get-Record) } 30

Stop-Everything

Say 'taking the files'
# A run that has already happened left a copy of the build that was current
# then. It is the only way back to it, so it is moved aside rather than
# overwritten.
if (Test-Path $backup) {
  $keep = Join-Path $Stage ('backup-' + (Get-Date -Format 'HHmmss'))
  Move-Item -Path $backup -Destination $keep -Force
  Say ('kept the earlier backup as ' + (Split-Path $keep -Leaf))
}
New-Item -ItemType Directory -Force -Path $backup | Out-Null
foreach ($name in $names) {
  $path = Join-Path $Release $name
  if (Test-Path $path) {
    Move-Aside $path (Join-Path $backup $name)
    Say ('backed up {0}' -f $name)
  }
}

try {
  Copy-Item (Join-Path $bin 'smartvpn-service-current.exe') $Release -Force
  foreach ($item in Get-ChildItem -Path $win -Force) {
    $dest = Join-Path $Release $item.Name
    # Copying a directory onto an existing directory of the same name nests it
    # rather than replacing it, so the destination is cleared first.
    if (Test-Path $dest) { Remove-Item $dest -Recurse -Force -ErrorAction SilentlyContinue }
    Copy-Item -Path $item.FullName -Destination $dest -Recurse -Force
  }
  Copy-Item (Join-Path $root 'backend\proxy.ps1') $Release -Force
} catch {
  Say ('copying the new build failed: ' + $_.Exception.Message)
  Restore-Previous
  Finish 1 'RESULT ok=false reason=copy'
}
Say 'the new files are in place'

if (-not (Start-App)) {
  Say 'the new service did not come up'
  Restore-Previous
  Finish 1 'RESULT ok=false reason=service'
}
if (-not $SkipConnect) {
  if (-not (Connect-Tunnel)) {
    Finish 2 ('RESULT ok=partial servicePid={0} - the new files are in place but nothing is connected' -f $newPid)
  }
}

Finish 0 ('RESULT ok=true servicePid={0} connected=true' -f $newPid)

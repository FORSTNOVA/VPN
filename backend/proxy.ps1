param(
  [Parameter(Mandatory = $true)][ValidateSet('enable', 'restore', 'status')][string]$Action,
  [Parameter(Mandatory = $true)][string]$BackupPath,
  [Parameter(Mandatory = $true)][string]$ProxyAddress
)

$ErrorActionPreference = 'Stop'
$keyPath = 'Software\Microsoft\Windows\CurrentVersion\Internet Settings'
$key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey($keyPath, $true)
if ($null -eq $key) { throw 'Could not open current-user Internet Settings.' }

function Get-ValueRecord([string]$Name) {
  $names = @($key.GetValueNames())
  if ($names -notcontains $Name) { return @{ present = $false; kind = ''; value = $null } }
  $kind = $key.GetValueKind($Name)
  $value = $key.GetValue($Name, $null, [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
  if ($kind -eq [Microsoft.Win32.RegistryValueKind]::Binary) {
    $value = [Convert]::ToBase64String($value)
  }
  return @{ present = $true; kind = $kind.ToString(); value = $value }
}

function Set-Record([string]$Name, $Record) {
  if (-not $Record.present) { $key.DeleteValue($Name, $false); return }
  $kind = [Enum]::Parse([Microsoft.Win32.RegistryValueKind], [string]$Record.kind)
  $value = $Record.value
  if ($kind -eq [Microsoft.Win32.RegistryValueKind]::Binary) { $value = [Convert]::FromBase64String([string]$value) }
  $key.SetValue($Name, $value, $kind)
}

function Same-Json($Left, $Right) {
  return (ConvertTo-Json -InputObject $Left -Compress -Depth 8) -ceq (ConvertTo-Json -InputObject $Right -Compress -Depth 8)
}

$managed = @('ProxyEnable', 'ProxyServer', 'ProxyOverride', 'AutoConfigURL', 'AutoDetect')
if ($Action -eq 'status') {
  $enabled = $key.GetValue('ProxyEnable', 0) -eq 1
  $server = [string]$key.GetValue('ProxyServer', '')
  $key.Close()
  @{ enabled = ($enabled -and $server -eq $ProxyAddress); server = $server } | ConvertTo-Json -Compress
  return
}
if ($Action -eq 'enable') {
  if (-not (Test-Path -LiteralPath $BackupPath)) {
    $original = @{}
    foreach ($name in $managed) { $original[$name] = Get-ValueRecord $name }
    $backup = @{ original = $original; appliedAddress = $ProxyAddress }
    $parent = Split-Path -Parent $BackupPath
    [void](New-Item -ItemType Directory -Force -Path $parent)
    [IO.File]::WriteAllText($BackupPath, (ConvertTo-Json -InputObject $backup -Depth 8), [Text.UTF8Encoding]::new($false))
  } else {
    $backup = Get-Content -LiteralPath $BackupPath -Raw | ConvertFrom-Json
    $backup | Add-Member -NotePropertyName appliedAddress -NotePropertyValue $ProxyAddress -Force
    [IO.File]::WriteAllText($BackupPath, (ConvertTo-Json -InputObject $backup -Depth 8), [Text.UTF8Encoding]::new($false))
  }
  $key.SetValue('ProxyEnable', 1, [Microsoft.Win32.RegistryValueKind]::DWord)
  $key.SetValue('ProxyServer', $ProxyAddress, [Microsoft.Win32.RegistryValueKind]::String)
  $key.SetValue('ProxyOverride', '<local>', [Microsoft.Win32.RegistryValueKind]::String)
  $key.DeleteValue('AutoConfigURL', $false)
  $key.SetValue('AutoDetect', 0, [Microsoft.Win32.RegistryValueKind]::DWord)
} else {
  if (Test-Path -LiteralPath $BackupPath) {
    $backup = Get-Content -LiteralPath $BackupPath -Raw | ConvertFrom-Json
    $appliedAddress = if ($backup.appliedAddress) { [string]$backup.appliedAddress } else { $ProxyAddress }
    $expected = @{
      ProxyEnable = @{ present = $true; kind = 'DWord'; value = 1 }
      ProxyServer = @{ present = $true; kind = 'String'; value = $appliedAddress }
      ProxyOverride = @{ present = $true; kind = 'String'; value = '<local>' }
      AutoConfigURL = @{ present = $false; kind = ''; value = $null }
      AutoDetect = @{ present = $true; kind = 'DWord'; value = 0 }
    }
    foreach ($name in $managed) {
      $current = Get-ValueRecord $name
      if (Same-Json $current $expected[$name]) { Set-Record $name $backup.original.$name }
    }
    Remove-Item -LiteralPath $BackupPath -Force
  }
}
$key.Close()

if (-not ('SmartVPNProxyNotify' -as [type])) {
  Add-Type @'
using System;
using System.Runtime.InteropServices;
public static class SmartVPNProxyNotify {
  [DllImport("user32.dll", CharSet=CharSet.Auto, SetLastError=true)]
  public static extern IntPtr SendMessageTimeout(IntPtr hWnd, uint Msg, UIntPtr wParam, string lParam, uint flags, uint timeout, out UIntPtr result);
}
'@
}
$result = [UIntPtr]::Zero
[void][SmartVPNProxyNotify]::SendMessageTimeout([IntPtr]0xffff, 0x001A, [UIntPtr]::Zero, 'Internet Settings', 2, 5000, [ref]$result)

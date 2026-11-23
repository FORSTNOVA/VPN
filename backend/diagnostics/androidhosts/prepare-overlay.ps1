[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$GoModCache,
    [Parameter(Mandatory)][string]$OutDir
)
$ErrorActionPreference = 'Stop'
$source = (Resolve-Path (Join-Path $GoModCache 'github.com/metacubex/mihomo@v1.19.31/component/resolver/hosts/hosts.go')).Path
$sourceHash = 'B0BA77363FC7A04BE3B37171763DF17B35E291BA785D8042C4762D6BF04C2F84'
if ((Get-FileHash $source).Hash -ne $sourceHash) { throw 'Unexpected Mihomo hosts source' }
$original = [IO.File]::ReadAllText($source)
$prefix = "func hasUpperCase(s string) bool {`n`t"
$from = $prefix + 'for i := range s {'
$to = $prefix + 'for i := 0; i < len(s); i++ {'
$updated = $original.Replace($from, $to)
if ($updated -eq $original) { throw 'Expected ASCII scan not found' }
$out = (New-Item -ItemType Directory -Path $OutDir).FullName
$replacement = Join-Path $out 'hosts.go'
[IO.File]::WriteAllText($replacement, $updated)
$overlay = Join-Path $out 'overlay.json'
[IO.File]::WriteAllText($overlay, (@{Replace = @{$source = $replacement}} | ConvertTo-Json))
Write-Output $overlay

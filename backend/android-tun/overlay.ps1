[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$GoExe,
    [Parameter(Mandatory)][string]$OutFile
)
$ErrorActionPreference = 'Stop'
Push-Location (Split-Path $PSScriptRoot)
function Get-PinnedModule($name, $version) {
    $module = (& $GoExe list -m -json $name | Out-String | ConvertFrom-Json)
    if ($LASTEXITCODE -or $module.Version -ne $version -or $module.PSObject.Properties['Replace']) {
        throw "Review the Android overlay against the selected $name dependency"
    }
    if (-not $module.PSObject.Properties['Dir']) {
        $module = (& $GoExe mod download -json "$name@$version" | Out-String | ConvertFrom-Json)
        if ($LASTEXITCODE) { throw "Could not locate pinned $name sources" }
    }
    return $module.Dir
}
try {
    $tun = Get-PinnedModule 'github.com/metacubex/sing-tun' 'v0.4.24'
    $mihomo = Get-PinnedModule 'github.com/metacubex/mihomo' 'v1.19.31'
    $provider = Join-Path $mihomo 'adapter/provider'
    $hashes = @{
        (Join-Path $provider 'healthcheck.go') = 'B3108EF1530C2BB28FF75928C64DC7150E68E2BB38F4F82E110EBDAFF1801C64'
        (Join-Path $tun 'tun_linux_gvisor.go') = '85E2CBD341EBA8B7A8ED6E9AFBB34CD3C1BF35AC5BDDBE814958C8F8B5504546'
        (Join-Path $tun 'stack_gvisor_filter.go') = '20ED825C56D6C8774F6D2DF2B1047E6010972650EE87618E0439199E03D126FE'
    }
    $replace = @{}
    foreach ($original in $hashes.Keys) {
        $file = Split-Path -Leaf $original
        if ((Get-FileHash -LiteralPath $original).Hash -ne $hashes[$original]) {
            throw "Unexpected upstream file: $original"
        }
        $replace[$original] = Join-Path $PSScriptRoot "$file.in"
    }
    [IO.File]::WriteAllText($OutFile, (@{Replace=$replace} | ConvertTo-Json))
} finally { Pop-Location }

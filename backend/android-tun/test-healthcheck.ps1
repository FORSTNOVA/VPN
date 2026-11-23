[CmdletBinding(DefaultParameterSetName='Run')]
param(
    [string]$GoExe = 'go',
    [Parameter(Mandatory)][string]$OutDir,
    [Parameter(ParameterSetName='Run')][ValidateRange(1,1000)][int]$Count = 1,
    [Parameter(Mandatory,ParameterSetName='Build')][switch]$BuildTranslatedArm64
)
$ErrorActionPreference = 'Stop'
$GoExe = (Get-Command $GoExe -CommandType Application).Source
$dir = (New-Item -Force -ItemType Directory -Path $OutDir).FullName
$source = Join-Path $dir 'healthcheck.go'
$test = Join-Path $dir 'healthcheck_test.go'
Copy-Item (Join-Path $PSScriptRoot 'healthcheck.go.in') $source
Copy-Item (Join-Path $PSScriptRoot 'healthcheck_test.go.in') $test
Push-Location (Split-Path $PSScriptRoot)
try {
    if ($BuildTranslatedArm64) {
        if ($env:GOOS -ne 'android' -or $env:GOARCH -ne 'arm64' -or $env:CGO_ENABLED -ne '1') {
            throw 'Set GOOS=android, GOARCH=arm64, CGO_ENABLED=1 and CC to the ARM64 Android NDK compiler.'
        }
        $goroot = & $GoExe env GOROOT
        if ($LASTEXITCODE) { throw 'Cannot locate Go runtime' }
        $original = Join-Path $goroot 'src/runtime/vdso_linux.go'
        if ((Get-FileHash -LiteralPath $original).Hash -ne 'A34771BE5B228378A2AB56BBF3897721004749DA0B469AD733F46F64EB64C17F') {
            throw 'Review the translated ARM64 vDSO workaround against the selected Go runtime.'
        }
        # Go 1.24.12 sets valid even without DT_VERDEF, then reads verdef.vd_flags.
        # Leave valid false to select the existing syscall fallback on this AVD.
        $patched = Join-Path $dir 'vdso_linux.go'
        $content = [IO.File]::ReadAllText($original).Replace("if info.verdef == nil {`n`t`tinfo.versym = nil", "if info.verdef == nil {`n`t`treturn")
        [IO.File]::WriteAllText($patched, $content)
        $overlay = Join-Path $dir 'vdso-overlay.json'
        [IO.File]::WriteAllText($overlay, (@{Replace=@{$original=$patched}} | ConvertTo-Json))
        & $GoExe test -c "-overlay=$overlay" -o (Join-Path $dir 'health-arm64.test') $source $test
        if ($LASTEXITCODE) { throw 'ARM64 health test build failed' }
        return
    }
    & $GoExe test "-count=$Count" $source $test
    if ($LASTEXITCODE) { throw 'Health check regression failed' }
} finally { Pop-Location }

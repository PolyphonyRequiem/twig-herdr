$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Strip-Verbatim([string]$Path) {
    if ($Path -and $Path.StartsWith('\\?\')) { return $Path.Substring(4) }
    return $Path
}

$ScriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$PluginRoot = if ($env:HERDR_PLUGIN_ROOT) { Strip-Verbatim $env:HERDR_PLUGIN_ROOT } else { Strip-Verbatim (Split-Path -Parent $ScriptRoot) }
$Bin = Join-Path $PluginRoot 'bin\twig-herdr.exe'

if (-not (Test-Path -LiteralPath $Bin)) {
    throw "twig-herdr: missing executable $Bin (run the install step first)"
}

& $Bin @args
exit $LASTEXITCODE

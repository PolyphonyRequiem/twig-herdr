$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Strip-Verbatim([string]$Path) {
    if ($Path -and $Path.StartsWith('\\?\')) { return $Path.Substring(4) }
    return $Path
}

function Fail([string]$Message) {
    throw "twig-herdr: $Message"
}

function Get-Version([string]$Manifest) {
    if (-not (Test-Path -LiteralPath $Manifest)) { Fail "manifest not found at $Manifest" }
    $match = Select-String -Path $Manifest -Pattern '^version = "([^"]+)"' | Select-Object -First 1
    if (-not $match) { Fail "could not read version from $Manifest" }
    return $match.Matches[0].Groups[1].Value
}

function Invoke-Download([string]$Url, [string]$Destination) {
    try {
        Invoke-WebRequest -Uri $Url -OutFile $Destination -UseBasicParsing -ErrorAction Stop | Out-Null
        return $true
    } catch {
        return $false
    }
}

function Get-Sha256([string]$Path) {
    try {
        return (Get-FileHash -Algorithm SHA256 -Path $Path).Hash.ToLowerInvariant()
    } catch {
        return $null
    }
}

$ScriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$PluginRoot = if ($env:HERDR_PLUGIN_ROOT) { Strip-Verbatim $env:HERDR_PLUGIN_ROOT } else { Strip-Verbatim (Split-Path -Parent $ScriptRoot) }
$Manifest = Join-Path $PluginRoot 'herdr-plugin.toml'
$Version = Get-Version $Manifest

$Arch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
if (-not $Arch) { Fail 'unsupported architecture on Windows' }
switch ($Arch.ToUpperInvariant()) {
    'AMD64' { $Arch = 'amd64' }
    'ARM64' { Fail 'Windows arm64 is not packaged by this release' }
    default { Fail "unsupported architecture $Arch on Windows" }
}

$Asset = "twig-herdr-windows-$Arch.exe"
$BaseUrl = "https://github.com/PolyphonyRequiem/twig-herdr/releases/download/v$Version"
$BinDir = Join-Path $PluginRoot 'bin'
$Dest = Join-Path $BinDir 'twig-herdr.exe'

$TmpDir = $null
try {
    New-Item -ItemType Directory -Path $BinDir -Force | Out-Null
    $TmpDir = Join-Path $BinDir ('.twig-herdr-' + [System.Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $TmpDir -Force | Out-Null

    $TmpBin = Join-Path $TmpDir $Asset
    $TmpSums = Join-Path $TmpDir 'SHA256SUMS'

    if (-not (Invoke-Download "$BaseUrl/$Asset" $TmpBin)) { Fail "prebuilt binary not available for v$Version ($Asset)" }
    if (-not (Invoke-Download "$BaseUrl/SHA256SUMS" $TmpSums)) { Fail "checksums not available for v$Version" }

    $Expected = $null
    Get-Content $TmpSums | ForEach-Object {
        if (-not $Expected -and $_ -match "^([0-9a-fA-F]{64}) [ *]$([regex]::Escape($Asset))$") {
            $Expected = $Matches[1].ToLowerInvariant()
        }
    }
    if (-not $Expected) { Fail "no checksum listed for $Asset" }

    $Actual = Get-Sha256 $TmpBin
    if (-not $Actual) { Fail 'no SHA-256 tool (Get-FileHash) available' }
    if ($Actual -ne $Expected) { Fail "checksum mismatch for $Asset (expected $Expected, got $Actual)" }

    if (Test-Path -LiteralPath $Dest) {
        [System.IO.File]::Replace($TmpBin, $Dest, $null)
    } else {
        [System.IO.File]::Move($TmpBin, $Dest)
    }
    Write-Host "twig-herdr: installed v$Version for windows/$Arch and verified SHA-256."
} catch {
    Write-Error $_.Exception.Message
    exit 1
} finally {
    if ($TmpDir -and (Test-Path -LiteralPath $TmpDir)) {
        Remove-Item -Recurse -Force $TmpDir -ErrorAction SilentlyContinue
    }
}

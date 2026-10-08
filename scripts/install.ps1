<#
.SYNOPSIS
Installs a checksummed portable Twig browser and its private native companion.
.EXAMPLE
./install.ps1 -Product twig-bench-tui -Version v0.3.0
.EXAMPLE
./install.ps1 -Product twig-bench-tui -Version latest -InstallDirectory "$HOME/.local/bin"
.DESCRIPTION
With no options, installs the checked-out Herdr plugin version into plugin-root/bin.
Portable release installs need no Go or .NET SDK. They never modify PATH, normal
Twig, shims, authentication, or workspace data. Existing workspaces must be migrated
explicitly with normal Twig; close old panels and acknowledge/reconnect afterward.
#>
param(
    [ValidateSet('twig-herdr', 'twig-bench-tui')]
    [ValidateNotNullOrEmpty()]
    [string]$Product = 'twig-herdr',
    [ValidateNotNullOrEmpty()]
    [string]$Version,
    [ValidateNotNullOrEmpty()]
    [string]$InstallDirectory
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$Product = $Product.ToLowerInvariant()

function Fail([string]$Message) { throw "${Product}: $Message" }
function Strip-Verbatim([string]$Path) {
    if ($Path -and $Path.StartsWith('\\?\')) { return $Path.Substring(4) }
    return $Path
}
function Get-Version([string]$Manifest) {
    if (-not (Test-Path -LiteralPath $Manifest -PathType Leaf)) { Fail "manifest not found at $Manifest" }
    $match = Select-String -LiteralPath $Manifest -Pattern '^version = "([^"]+)"' | Select-Object -First 1
    if (-not $match) { Fail "could not read version from $Manifest" }
    return $match.Matches[0].Groups[1].Value
}
function Download([string]$Url, [string]$Destination) {
    Invoke-WebRequest -Uri $Url -OutFile $Destination -UseBasicParsing -ErrorAction Stop | Out-Null
}
function Assert-Help([string]$Executable, [string[]]$Arguments, [string[]]$Flags) {
    $preference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $output = & $Executable @Arguments --help 2>&1 | Out-String
        $exitCode = $LASTEXITCODE
    } finally { $ErrorActionPreference = $preference }
    if ($exitCode -ne 0) { Fail "bundled executable cannot run: $($Arguments -join ' ') --help" }
    foreach ($flag in $Flags) {
        if (-not $output.Contains($flag)) { Fail "bundled companion lacks $flag on $($Arguments -join ' '); installation unchanged" }
    }
}
function Expand-SafeBundle([string]$Archive, [string]$Destination, [string]$BrowserName) {
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $zip = [System.IO.Compression.ZipFile]::OpenRead($Archive)
    try {
        $names = New-Object 'System.Collections.Generic.HashSet[string]' ([StringComparer]::OrdinalIgnoreCase)
        foreach ($entry in $zip.Entries) {
            $name = $entry.FullName
            $mode = ($entry.ExternalAttributes -shr 16) -band 0xF000
            if ($name -match '\\|(^|/)\.\.?(/|$)|//|^[A-Za-z]:|^/' -or $name -notmatch '^[A-Za-z0-9._+/-]+$' -or $mode -notin @(0, 0x8000, 0x4000)) {
                Fail "unsafe archive entry: $name"
            }
            if ($name -ne $BrowserName -and $name -ne 'INSTALL.txt' -and $name -ne 'twig-bench-native/' -and -not $name.StartsWith('twig-bench-native/')) {
                Fail "unexpected archive entry: $name"
            }
            if (-not $names.Add($name.TrimEnd('/'))) { Fail "duplicate archive entry: $name" }
        }
        foreach ($entry in $zip.Entries) {
            $path = Join-Path $Destination ($entry.FullName.Replace('/', '\'))
            if ($entry.FullName.EndsWith('/')) {
                [System.IO.Directory]::CreateDirectory($path) | Out-Null
            } else {
                [System.IO.Directory]::CreateDirectory((Split-Path -Parent $path)) | Out-Null
                $inputStream = $entry.Open()
                try {
                    $outputStream = [System.IO.File]::Open($path, [System.IO.FileMode]::CreateNew)
                    try { $inputStream.CopyTo($outputStream) } finally { $outputStream.Dispose() }
                } finally { $inputStream.Dispose() }
            }
        }
    } finally { $zip.Dispose() }
}
function Assert-NotLink([string]$Path) {
    if (Test-Path -LiteralPath $Path) {
        $item = Get-Item -LiteralPath $Path -Force
        if ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) { Fail "refusing to replace a link: $Path" }
    }
}

$Temporary = $null
$Lock = $null
$LockPath = $null
$KeepTemporary = $false
try {
    if ($Version -and $Version -ne 'latest' -and $Version -notmatch '^v?[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9][A-Za-z0-9.-]*)?$') {
        Fail 'Version must be latest or a release version such as v0.3.0'
    }
    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { Fail 'use install.sh on Linux or macOS' }
    $arch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
    if ($arch -ne 'AMD64') { Fail "unsupported Windows architecture $arch; this release supports Windows amd64 only" }

    if ($Product -eq 'twig-herdr' -and -not $InstallDirectory) {
        $pluginRoot = if ($env:HERDR_PLUGIN_ROOT) { Strip-Verbatim $env:HERDR_PLUGIN_ROOT } else { Split-Path -Parent $PSScriptRoot }
        $manifestVersion = Get-Version (Join-Path $pluginRoot 'herdr-plugin.toml')
        if ($Version -and $Version.TrimStart('v') -ne $manifestVersion) { Fail 'plugin install version must match the checked-out manifest' }
        $Version = $manifestVersion
        $InstallDirectory = Join-Path $pluginRoot 'bin'
    } else {
        if (-not $InstallDirectory) { $InstallDirectory = Join-Path $HOME '.local\bin' }
        if (-not $Version) { $Version = 'latest' }
    }
    if ($Version -ne 'latest' -and $Version -notmatch '^v?[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9][A-Za-z0-9.-]*)?$') {
        Fail 'Version must be latest or a release version such as v0.3.0'
    }
    if ([string]::IsNullOrWhiteSpace($InstallDirectory) -or $InstallDirectory -match '[\r\n]') { Fail 'invalid install directory' }
    $InstallDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath((Strip-Verbatim $InstallDirectory))
    $baseUrl = if ($Version -eq 'latest') { 'https://github.com/PolyphonyRequiem/twig-herdr/releases/latest/download' } else {
        "https://github.com/PolyphonyRequiem/twig-herdr/releases/download/v$($Version.TrimStart('v'))"
    }
    $asset = "$Product-windows-amd64.zip"
    $browserName = "$Product.exe"
    [System.IO.Directory]::CreateDirectory($InstallDirectory) | Out-Null
    $Temporary = Join-Path $InstallDirectory ('.twig-bench-install-' + [guid]::NewGuid().ToString('N'))
    [System.IO.Directory]::CreateDirectory($Temporary) | Out-Null
    $archive = Join-Path $Temporary $asset
    $sums = Join-Path $Temporary 'SHA256SUMS'
    Download "$baseUrl/$asset" $archive
    Download "$baseUrl/SHA256SUMS" $sums
    $hashes = @(Get-Content -LiteralPath $sums | ForEach-Object {
        if ($_ -match "^([0-9a-fA-F]{64}) [ *]$([regex]::Escape($asset))$") { $Matches[1].ToLowerInvariant() }
    })
    if ($hashes.Count -ne 1) { Fail "expected exactly one SHA256SUMS entry for $asset" }
    if ((Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant() -ne $hashes[0]) { Fail "checksum mismatch for $asset; installation unchanged" }

    $stage = Join-Path $Temporary 'bundle'
    [System.IO.Directory]::CreateDirectory($stage) | Out-Null
    Expand-SafeBundle $archive $stage $browserName
    foreach ($required in @($browserName, 'INSTALL.txt', 'twig-bench-native/twig-bench-native.exe', 'twig-bench-native/e_sqlite3.dll')) {
        if (-not (Test-Path -LiteralPath (Join-Path $stage $required) -PathType Leaf)) { Fail "bundle missing $required" }
    }
    Push-Location $stage
    try {
        Assert-Help (Join-Path $stage $browserName) @() @($Product)
        $native = Join-Path $stage 'twig-bench-native/twig-bench-native.exe'
        Assert-Help $native @('workspace') @('--include-browser', '--expect-binding', '--expect-identity')
        Assert-Help $native @('workspace', 'track') @('--expect-bench', '--expect-settings', '--expect-binding', '--expect-identity')
        Assert-Help $native @('workspace', 'track-tree') @('--expect-bench', '--expect-settings', '--expect-binding', '--expect-identity')
        Assert-Help $native @('workspace', 'untrack') @('--mode', '--expect-bench', '--expect-settings', '--expect-binding', '--expect-identity')
        Assert-Help $native @('workspace', 'sync') @('--expect-bench', '--expect-binding', '--expect-identity')
        Assert-Help $native @('bench', 'configuration') @('--expect-bench', '--expect-binding', '--expect-identity')
        Assert-Help $native @('bench', 'configuration', 'area', 'add') @('--expect-bench', '--expect-settings', '--expect-binding', '--expect-identity')
        Assert-Help $native @('bench', 'configuration', 'sprint', 'remove') @('--expect-bench', '--expect-settings', '--expect-binding', '--expect-identity')
        Assert-Help $native @('bench', 'list') @('--include-management', '--expect-binding', '--expect-identity')
        Assert-Help $native @('bench', 'create') @('--expect-binding', '--expect-identity')
        Assert-Help $native @('bench', 'switch') @('--expect-bench', '--expect-binding', '--expect-identity')
        Assert-Help $native @('bench', 'delete') @('--expect-bench', '--expect-contents', '--confirm', '--expect-binding', '--expect-identity')
    } finally { Pop-Location }

    # Stage and validate everything before acquiring the shared companion install lock.
    $LockPath = Join-Path $InstallDirectory '.twig-bench-install.lock'
    $Lock = [System.IO.File]::Open($LockPath, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
    $backup = Join-Path $Temporary 'previous'
    [System.IO.Directory]::CreateDirectory($backup) | Out-Null
    $movedOld = New-Object 'System.Collections.Generic.List[string]'
    $installed = New-Object 'System.Collections.Generic.List[string]'
    $names = @('twig-bench-native', $browserName)
    foreach ($name in $names) {
        $destination = Join-Path $InstallDirectory $name
        Assert-NotLink $destination
        if (Test-Path -LiteralPath $destination) {
            $isDirectory = (Get-Item -LiteralPath $destination).PSIsContainer
            if ($isDirectory -ne ($name -eq 'twig-bench-native')) { Fail "unexpected destination type: $destination" }
        }
    }
    try {
        foreach ($name in $names) {
            $destination = Join-Path $InstallDirectory $name
            if (Test-Path -LiteralPath $destination) {
                Move-Item -LiteralPath $destination -Destination (Join-Path $backup $name)
                $movedOld.Add($name)
            }
        }
        foreach ($name in $names) {
            Move-Item -LiteralPath (Join-Path $stage $name) -Destination (Join-Path $InstallDirectory $name)
            $installed.Add($name)
        }
    } catch {
        $installError = $_
        try {
            foreach ($name in $installed) { Remove-Item -LiteralPath (Join-Path $InstallDirectory $name) -Recurse -Force }
            foreach ($name in $movedOld) { Move-Item -LiteralPath (Join-Path $backup $name) -Destination (Join-Path $InstallDirectory $name) }
        } catch {
            $KeepTemporary = $true
            Write-Warning "Rollback needs attention; previous files preserved at $backup. $($_.Exception.Message)"
        }
        throw $installError
    }
    Write-Host "${Product}: installed $Version for windows/amd64; whole bundle SHA-256 and native browser/lifecycle capabilities verified."
    Write-Host "Run $(Join-Path $InstallDirectory $browserName) from an existing Twig workspace. No PATH, normal Twig, shims, or workspace data were changed."
    Write-Host 'Close old browser panels before upgrading; migrate only explicitly with normal Twig, then acknowledge/reconnect (Ctrl+R).'
} catch {
    Write-Error $_.Exception.Message -ErrorAction Continue
    exit 1
} finally {
    if ($Lock) {
        $Lock.Dispose()
        Remove-Item -LiteralPath $LockPath -Force -ErrorAction SilentlyContinue
    }
    if ($Temporary -and -not $KeepTemporary -and (Test-Path -LiteralPath $Temporary)) {
        Remove-Item -LiteralPath $Temporary -Recurse -Force -ErrorAction SilentlyContinue
    }
}

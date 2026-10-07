param(
    [string]$InstallDirectory = (Join-Path $HOME '.local\bin'),
    [string]$NativeCompanionPath
)
$ErrorActionPreference = 'Stop'
$InstallDirectory = [System.IO.Path]::GetFullPath($InstallDirectory)
$Root = Split-Path -Parent $PSScriptRoot
$Destination = Join-Path $InstallDirectory 'twig-bench-tui.exe'
$NativeDirectory = Join-Path $InstallDirectory 'twig-bench-native'
$NativeDestination = Join-Path $NativeDirectory 'twig-bench-native.exe'

function Assert-BenchCompanion([string]$Path) {
    $NativeHelp = & $Path workspace --help 2>&1 | Out-String
    if ($LASTEXITCODE -ne 0 -or $NativeHelp -notmatch '--include-browser') {
        throw 'Native companion must support workspace --include-browser. Publish the updated Twig source first.'
    }
    $ConfigurationHelp = & $Path bench configuration --help 2>&1 | Out-String
    if ($LASTEXITCODE -ne 0 -or $ConfigurationHelp -notmatch '--expect-bench') {
        throw 'Native companion must support guarded bench configuration. Publish the updated Twig source first.'
    }
    $UnpinHelp = & $Path workspace untrack --help 2>&1 | Out-String
    if ($LASTEXITCODE -ne 0 -or $UnpinHelp -notmatch '--mode') {
        throw 'Native companion must support type-specific workspace untrack --mode. Publish the updated Twig source first.'
    }
    if (-not (Test-Path -LiteralPath (Join-Path (Split-Path -Parent $Path) 'e_sqlite3.dll') -PathType Leaf)) {
        throw 'Native companion must include its e_sqlite3.dll dependency.'
    }
}

if ($NativeCompanionPath) {
    $NativeCompanionPath = [System.IO.Path]::GetFullPath($NativeCompanionPath)
    if (-not (Test-Path -LiteralPath $NativeCompanionPath -PathType Leaf)) {
        throw "Native companion not found: $NativeCompanionPath"
    }
    Assert-BenchCompanion $NativeCompanionPath
    $SqliteSource = Join-Path (Split-Path -Parent $NativeCompanionPath) 'e_sqlite3.dll'
    if (-not (Test-Path -LiteralPath $SqliteSource -PathType Leaf)) {
        throw 'Published native companion must include its e_sqlite3.dll dependency.'
    }
} elseif (-not (Test-Path -LiteralPath $NativeDestination)) {
    throw 'Supply -NativeCompanionPath pointing to the updated published Twig executable; the browser requires its semantic workspace contract.'
} else {
    Assert-BenchCompanion $NativeDestination
}

function Install-Binary([string]$Source, [string]$Target) {
    $Stage = $Target + '.new-' + [guid]::NewGuid().ToString('N')
    $Backup = $Target + '.previous-' + [guid]::NewGuid().ToString('N')
    Copy-Item -LiteralPath $Source -Destination $Stage
    try {
        if (Test-Path -LiteralPath $Target) { [System.IO.File]::Move($Target, $Backup) }
        try { [System.IO.File]::Move($Stage, $Target) }
        catch {
            if (-not (Test-Path -LiteralPath $Target) -and (Test-Path -LiteralPath $Backup)) {
                [System.IO.File]::Move($Backup, $Target)
            }
            throw
        }
    } finally {
        if (Test-Path -LiteralPath $Stage) { Remove-Item -LiteralPath $Stage -Force }
        if (Test-Path -LiteralPath $Backup) {
            try { Remove-Item -LiteralPath $Backup -Force }
            catch { Write-Warning "Running previous image retained at $Backup; remove it after closing that browser." }
        }
    }
}
$Temporary = Join-Path ([System.IO.Path]::GetTempPath()) ('twig-bench-tui-' + [guid]::NewGuid().ToString('N') + '.exe')
Push-Location $Root
try {
    & go build -trimpath -o $Temporary ./cmd/twig-bench-tui
    if ($LASTEXITCODE -ne 0) { throw 'Standalone browser build failed' }
    New-Item -ItemType Directory -Force -Path $InstallDirectory | Out-Null
    if ($NativeCompanionPath) {
        New-Item -ItemType Directory -Force -Path $NativeDirectory | Out-Null
        Install-Binary $SqliteSource (Join-Path $NativeDirectory 'e_sqlite3.dll')
        Install-Binary $NativeCompanionPath $NativeDestination
    }
    Install-Binary $Temporary $Destination
    $UserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (($UserPath -split ';') -notcontains $InstallDirectory) {
        [Environment]::SetEnvironmentVariable('Path', "$InstallDirectory;$UserPath", 'User')
    }
    if (($env:PATH -split ';') -notcontains $InstallDirectory) {
        $env:PATH = "$InstallDirectory;$env:PATH"
    }
    Write-Host "Installed $Destination. Run twig-bench-tui from a Twig workspace in Windows Terminal (reopen existing terminals if PATH changed)."
} finally {
    Pop-Location
    if (Test-Path -LiteralPath $Temporary) { Remove-Item -LiteralPath $Temporary -Force }
}

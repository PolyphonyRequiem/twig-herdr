param(
    [string]$InstallDirectory = (Join-Path $HOME '.local\bin')
)
$ErrorActionPreference = 'Stop'
$InstallDirectory = [System.IO.Path]::GetFullPath($InstallDirectory)
$Root = Split-Path -Parent $PSScriptRoot
$Destination = Join-Path $InstallDirectory 'twig-bench-tui.exe'
$Temporary = Join-Path ([System.IO.Path]::GetTempPath()) ('twig-bench-tui-' + [guid]::NewGuid().ToString('N') + '.exe')
Push-Location $Root
try {
    & go build -trimpath -o $Temporary ./cmd/twig-bench-tui
    if ($LASTEXITCODE -ne 0) { throw 'Standalone browser build failed' }
    New-Item -ItemType Directory -Force -Path $InstallDirectory | Out-Null
    Copy-Item -LiteralPath $Temporary -Destination $Destination -Force
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

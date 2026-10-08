param(
    [Parameter(Mandatory = $true)][string]$FrontendDirectory,
    [Parameter(Mandatory = $true)][string]$NativeDirectory,
    [string]$ExpectedPublisher = 'CN=Daniel Green, O=Daniel Green, L=Kirkland, S=wa, C=US'
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$frontend = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($FrontendDirectory)
$native = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($NativeDirectory)
$files = @(
    (Join-Path $frontend 'twig-herdr.exe'),
    (Join-Path $frontend 'twig-bench-tui.exe'),
    (Join-Path $native 'twig.exe')
)
$evidence = foreach ($file in $files) {
    if (-not (Test-Path -LiteralPath $file -PathType Leaf)) { throw "Missing required signed executable: $file" }
    $signature = Get-AuthenticodeSignature -LiteralPath $file
    if ($signature.Status -ne 'Valid') { throw "Authenticode verification failed for $file`: $($signature.Status)" }
    if ($signature.SignerCertificate.Subject -ne $ExpectedPublisher) { throw "Unexpected publisher for $file`: $($signature.SignerCertificate.Subject)" }
    if (-not $signature.SignerCertificate.Issuer.StartsWith('CN=Microsoft ID Verified CS AOC CA')) { throw "Unexpected Trusted Signing issuer for $file" }
    if ($null -eq $signature.TimeStamperCertificate) { throw "Missing trusted timestamp for $file" }
    [pscustomobject]@{
        File = [System.IO.Path]::GetFileName($file)
        Status = $signature.Status.ToString()
        Publisher = $signature.SignerCertificate.Subject
        Issuer = $signature.SignerCertificate.Issuer
        TimestampIssuer = $signature.TimeStamperCertificate.Issuer
    }
}
$evidence | ConvertTo-Json -Depth 3

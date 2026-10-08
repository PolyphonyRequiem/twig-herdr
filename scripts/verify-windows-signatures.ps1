param(
    [Parameter(Mandatory = $true)][string]$FrontendDirectory,
    [Parameter(Mandatory = $true)][string]$NativeDirectory
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$ExpectedPublisher = 'CN=Daniel Green, O=Daniel Green, L=Kirkland, S=wa, C=US'
# Artifact Signing renews certificates daily; issuer DNs are not durable pins.
# https://learn.microsoft.com/azure/artifact-signing/concept-certificate-management
$ExpectedProfileEku = '1.3.6.1.4.1.311.97.526284689.305431252.158505691.772257111'
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
    $ekuValues = @($signature.SignerCertificate.Extensions |
        Where-Object { $_.Oid.Value -eq '2.5.29.37' } |
        ForEach-Object { $_.EnhancedKeyUsages } |
        ForEach-Object { $_.Value })
    if ($ekuValues -notcontains '1.3.6.1.4.1.311.97.1.0' -or $ekuValues -notcontains $ExpectedProfileEku) {
        throw "Unexpected Artifact Signing public-trust profile for $file ($($signature.SignerCertificate.Issuer))"
    }
    if ($null -eq $signature.TimeStamperCertificate) { throw "Missing trusted timestamp for $file" }
    [pscustomobject]@{
        File = [System.IO.Path]::GetFileName($file)
        Status = $signature.Status.ToString()
        Publisher = $signature.SignerCertificate.Subject
        Issuer = $signature.SignerCertificate.Issuer
        TimestampIssuer = $signature.TimeStamperCertificate.Issuer
        ProfileEku = $ExpectedProfileEku
    }
}
$evidence | ConvertTo-Json -Depth 3

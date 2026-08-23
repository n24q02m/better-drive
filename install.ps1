# better-drive one-shot installer for Windows (PowerShell 5+).
# Usage:
#   iwr -useb https://raw.githubusercontent.com/n24q02m/better-drive/main/install.ps1 | iex
#   iwr -useb https://raw.githubusercontent.com/n24q02m/better-drive/main/install.ps1 | iex; & install -Version v1.0.0
# Flags:
#   -Version <tag>   install a specific release tag (default: latest)
#   -Prefix <path>   install target dir (default: $env:LOCALAPPDATA\Programs\better-drive)
#   -Quiet           suppress progress output

#Requires -Version 5.0
[CmdletBinding()]
[Diagnostics.CodeAnalysis.SuppressMessageAttribute('PSReviewUnusedParameter', 'Quiet', Justification='Used in Log closure via script scope')]
[Diagnostics.CodeAnalysis.SuppressMessageAttribute('PSAvoidUsingWriteHost', '', Justification='Installer progress output goes to host, not pipeline')]
param(
    [string]$Version = "",
    [string]$Prefix = "",
    [switch]$Quiet
)

$ErrorActionPreference = "Stop"
$Repo = "n24q02m/better-drive"

function Log($msg) { if (-not $Quiet) { Write-Host "==> $msg" } }
function Die($msg) { Write-Error "better-drive install: $msg"; exit 1 }

if (-not [System.Environment]::Is64BitOperatingSystem) {
    Die "32-bit Windows is not supported"
}

$arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }

if (-not $Version) {
    Log "Detecting latest release"
    try {
        $latest = Invoke-RestMethod "https://api.github.com/repos/$Repo/releases/latest"
        $Version = $latest.tag_name
    } catch {
        Die "could not detect latest version: $($_.Exception.Message)"
    }
}

$verTrim = $Version -replace '^v', ''

if (-not $Prefix) {
    $Prefix = Join-Path $env:LOCALAPPDATA "Programs\better-drive"
}

$asset       = "better-drive_${verTrim}_windows_${arch}.zip"
$url         = "https://github.com/$Repo/releases/download/$Version/$asset"
$checksumUrl = "https://github.com/$Repo/releases/download/$Version/checksums.txt"
$certUrl     = "https://github.com/$Repo/releases/download/$Version/checksums.txt.pem"
$sigUrl      = "https://github.com/$Repo/releases/download/$Version/checksums.txt.sig"

$tmp = Join-Path $env:TEMP ("better-drive-install-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp -Force | Out-Null
function Expand-SafeZip($zipPath, $destination) {
    Add-Type -AssemblyName System.IO.Compression
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $root = [System.IO.Path]::GetFullPath($destination)
    $archive = [System.IO.Compression.ZipFile]::OpenRead($zipPath)
    try {
        if ($archive.Entries.Count -gt 1000) { Die "archive contains too many entries" }
        foreach ($entry in $archive.Entries) {
            if ($entry.Length -gt 100MB) { Die "archive entry is too large: $($entry.FullName)" }
            $relative = $entry.FullName.Replace('/', '\')
            if ([System.IO.Path]::IsPathRooted($relative) -or $relative -match '(^|\\)\.\.(\\|$)' -or $relative -match '(^|\\)(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(\.|\\|$)') {
                Die "unsafe archive path: $($entry.FullName)"
            }
            $target = [System.IO.Path]::GetFullPath((Join-Path $destination $relative))
            if (-not $target.StartsWith($root.TrimEnd('\') + '\', [System.StringComparison]::OrdinalIgnoreCase)) {
                Die "archive path escapes destination: $($entry.FullName)"
            }
            if (($entry.ExternalAttributes -band 0xF000) -eq 0xA000) {
                Die "archive symlink is forbidden: $($entry.FullName)"
            }
        }
        New-Item -ItemType Directory -Path $destination -Force | Out-Null
        foreach ($entry in $archive.Entries) {
            if ($entry.FullName.EndsWith('/')) {
                New-Item -ItemType Directory -Path (Join-Path $destination $entry.FullName.Replace('/', '\')) -Force | Out-Null
                continue
            }
            $target = [System.IO.Path]::GetFullPath((Join-Path $destination $entry.FullName.Replace('/', '\')))
            New-Item -ItemType Directory -Path ([System.IO.Path]::GetDirectoryName($target)) -Force | Out-Null
            $input = $entry.Open()
            try {
                $output = [System.IO.File]::Open($target, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
                try { $input.CopyTo($output) } finally { $output.Dispose() }
            } finally { $input.Dispose() }
        }
    } finally {
        $archive.Dispose()
    }
}

New-Item -ItemType Directory -Path $tmp -Force | Out-Null
try {
    Log "Downloading $asset"
    Invoke-WebRequest $url -OutFile (Join-Path $tmp "better-drive.zip") -UseBasicParsing
    Invoke-WebRequest $checksumUrl -OutFile (Join-Path $tmp "checksums.txt") -UseBasicParsing

    Log "Verifying SHA256 checksum"
    $actual = (Get-FileHash (Join-Path $tmp "better-drive.zip") -Algorithm SHA256).Hash.ToLower()
    $expectedRow = (Get-Content (Join-Path $tmp "checksums.txt") | Select-String $asset | Select-Object -First 1)
    if (-not $expectedRow) { Die "no checksum row for $asset in checksums.txt" }
    $expected = ($expectedRow.ToString() -split '\s+')[0]
    if ($expected -ne $actual) {
        Die "checksum mismatch (expected $expected, got $actual)"
    }

    $cosign = Get-Command cosign -ErrorAction SilentlyContinue
    if (-not $cosign) {
        Die "cosign is required to verify the published Sigstore bundle"
    }
    Log "Verifying cosign Sigstore signature"
    Invoke-WebRequest $certUrl -OutFile (Join-Path $tmp "checksums.txt.pem") -UseBasicParsing
    Invoke-WebRequest $sigUrl  -OutFile (Join-Path $tmp "checksums.txt.sig") -UseBasicParsing
    & $cosign.Source verify-blob `
        --certificate (Join-Path $tmp "checksums.txt.pem") `
        --signature   (Join-Path $tmp "checksums.txt.sig") `
        --certificate-identity-regexp "https://github.com/$Repo/.+" `
        --certificate-oidc-issuer "https://token.actions.githubusercontent.com" `
        (Join-Path $tmp "checksums.txt") 2>&1 | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Die "cosign Sigstore verification failed"
    }

    Log "Extracting through SAFE-ARCHIVE-V1 preflight"
    $extractRoot = Join-Path $tmp "extract"
    Expand-SafeZip (Join-Path $tmp "better-drive.zip") $extractRoot
    $extracted = Join-Path $extractRoot "better-drive.exe"
    if (-not (Test-Path -LiteralPath $extracted -PathType Leaf)) {
        Die "archive does not contain better-drive.exe at its root"
    }
    New-Item -ItemType Directory -Path $Prefix -Force | Out-Null
    Copy-Item -LiteralPath $extracted -Destination (Join-Path $Prefix "better-drive.exe") -Force

    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($userPath -notlike "*$Prefix*") {
        [Environment]::SetEnvironmentVariable("Path", "$userPath;$Prefix", "User")
        Log "Added $Prefix to user PATH (restart shell to apply)"
    }

    $installed = & (Join-Path $Prefix "better-drive.exe") --version
    Log "Installed: $installed"
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

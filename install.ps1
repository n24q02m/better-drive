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

function Assert-NoSymlinkAncestors($targetPath) {
    $full = [System.IO.Path]::GetFullPath($targetPath)
    $curr = $full
    while ($curr -and ($curr -ne [System.IO.Path]::GetPathRoot($curr))) {
        if (Test-Path -LiteralPath $curr) {
            $item = Get-Item -LiteralPath $curr -Force -ErrorAction SilentlyContinue
            if ($item -and ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint)) {
                Die "ancestor path is a forbidden symlink or reparse point: $curr"
            }
        }
        $parent = [System.IO.Path]::GetDirectoryName($curr)
        if ($parent -eq $curr) { break }
        $curr = $parent
    }
}

function Ensure-SecureDirectory($dirPath) {
    Assert-NoSymlinkAncestors $dirPath
    if (-not (Test-Path -LiteralPath $dirPath)) {
        New-Item -ItemType Directory -Path $dirPath -Force | Out-Null
    }
    $dirInfo = Get-Item -LiteralPath $dirPath -Force
    if ($dirInfo.Attributes -band [System.IO.FileAttributes]::ReparsePoint) {
        Die "directory is a reparse point: $dirPath"
    }
}

function Expand-SafeZip($zipPath, $destination) {
    Add-Type -AssemblyName System.IO.Compression
    Add-Type -AssemblyName System.IO.Compression.FileSystem

    Ensure-SecureDirectory $destination
    $root = [System.IO.Path]::GetFullPath($destination)
    $archive = [System.IO.Compression.ZipFile]::OpenRead($zipPath)
    try {
        if ($archive.Entries.Count -gt 1000) {
            Die "archive contains too many entries ($($archive.Entries.Count) > 1000)"
        }

        $seenEntries = New-Object 'System.Collections.Generic.HashSet[string]' ([System.StringComparer]::OrdinalIgnoreCase)
        $totalBytes = 0

        foreach ($entry in $archive.Entries) {
            $rawName = $entry.FullName
            if ([string]::IsNullOrWhiteSpace($rawName)) {
                Die "archive contains empty entry name"
            }

            # Check duplicate entries
            $normalizedKey = $rawName.TrimEnd('/')
            if (-not $seenEntries.Add($normalizedKey)) {
                Die "archive contains duplicate entry: $rawName"
            }

            # Check individual entry size limit (100MB)
            if ($entry.Length -gt 100MB) {
                Die "archive entry exceeds size limit of 100MB: $rawName ($($entry.Length) bytes)"
            }
            $totalBytes += $entry.Length
            if ($totalBytes -gt 500MB) {
                Die "aggregate archive uncompressed size exceeds limit of 500MB"
            }

            # Path safety checks
            if ($rawName.Contains("`0")) {
                Die "archive entry contains null character: $rawName"
            }

            $relative = $rawName.Replace('/', '\')
            if ([System.IO.Path]::IsPathRooted($relative) -or $relative.StartsWith('\\') -or $relative -match '^[a-zA-Z]:') {
                Die "unsafe absolute or rooted archive path: $rawName"
            }

            if ($relative -match '(^|\\)\.\.(\\|$)' -or $relative -match '(^|\\)\.(\\|$)' -and ($relative -ne '.')) {
                Die "unsafe archive traversal path: $rawName"
            }

            if ($relative -match '(^|\\)(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(\.|$|\\)') {
                Die "archive contains forbidden Windows device name: $rawName"
            }

            $target = [System.IO.Path]::GetFullPath((Join-Path $destination $relative))
            if (-not $target.StartsWith($root.TrimEnd('\') + '\', [System.StringComparison]::OrdinalIgnoreCase) -and ($target -ne $root)) {
                Die "archive path escapes destination root: $rawName"
            }

            # Reject symlinks and special attributes
            if (($entry.ExternalAttributes -band 0xF000) -eq 0xA000) {
                Die "archive symlink is forbidden: $rawName"
            }
        }

        # Safe extraction of entries
        foreach ($entry in $archive.Entries) {
            if ($entry.FullName.EndsWith('/')) {
                $dirTarget = [System.IO.Path]::GetFullPath((Join-Path $destination $entry.FullName.Replace('/', '\')))
                Ensure-SecureDirectory $dirTarget
                continue
            }

            $target = [System.IO.Path]::GetFullPath((Join-Path $destination $entry.FullName.Replace('/', '\')))
            $targetDir = [System.IO.Path]::GetDirectoryName($target)
            Ensure-SecureDirectory $targetDir

            $inStream = $entry.Open()
            try {
                $outStream = [System.IO.File]::Open($target, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
                try {
                    $inStream.CopyTo($outStream)
                } finally {
                    $outStream.Dispose()
                }
            } finally {
                $inStream.Dispose()
            }
        }

        # Verify allowlisted files and structure
        $extractedItems = Get-ChildItem -LiteralPath $destination -Force
        foreach ($item in $extractedItems) {
            $name = $item.Name
            if ($name -match '^(better-drive\.exe|LICENSE.*|README.*|CHANGELOG.*)$') {
                continue
            }
            Die "archive contains unexpected non-allowlisted file: $name"
        }
    } finally {
        $archive.Dispose()
    }
}

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

$asset             = "better-drive_${verTrim}_windows_${arch}.zip"
$url               = "https://github.com/$Repo/releases/download/$Version/$asset"
$checksumUrl       = "https://github.com/$Repo/releases/download/$Version/checksums.txt"
$sigstoreBundleUrl = "https://github.com/$Repo/releases/download/$Version/checksums.txt.sigstore.json"

$tmp = Join-Path $env:TEMP ("better-drive-install-" + [guid]::NewGuid())
Ensure-SecureDirectory $tmp

try {
    Log "Downloading $asset"
    Invoke-WebRequest $url -OutFile (Join-Path $tmp "better-drive.zip") -UseBasicParsing
    Invoke-WebRequest $checksumUrl -OutFile (Join-Path $tmp "checksums.txt") -UseBasicParsing
    Invoke-WebRequest $sigstoreBundleUrl -OutFile (Join-Path $tmp "checksums.txt.sigstore.json") -UseBasicParsing

    Log "Verifying SHA256 checksum"
    $actual = (Get-FileHash (Join-Path $tmp "better-drive.zip") -Algorithm SHA256).Hash.ToLower()
    $expectedRow = (Get-Content (Join-Path $tmp "checksums.txt") | Select-String $asset | Select-Object -First 1)
    if (-not $expectedRow) { Die "no checksum row for $asset in checksums.txt" }
    $expected = ($expectedRow.ToString() -split '\s+')[0].ToLower()
    if ($expected -ne $actual) {
        Die "checksum mismatch (expected $expected, got $actual)"
    }

    $cosign = Get-Command cosign -ErrorAction SilentlyContinue
    if (-not $cosign) {
        Die "cosign is required to verify the published Sigstore bundle"
    }

    Log "Verifying cosign Sigstore bundle signature"
    & $cosign.Source verify-blob `
        --bundle (Join-Path $tmp "checksums.txt.sigstore.json") `
        --certificate-identity-regexp "https://github.com/$Repo/.+" `
        --certificate-oidc-issuer "https://token.actions.githubusercontent.com" `
        (Join-Path $tmp "checksums.txt") 2>&1 | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Die "cosign Sigstore bundle verification failed"
    }

    Log "Extracting through SAFE-ARCHIVE-V1 preflight"
    $extractRoot = Join-Path $tmp "extract"
    Expand-SafeZip (Join-Path $tmp "better-drive.zip") $extractRoot

    $extracted = Join-Path $extractRoot "better-drive.exe"
    if (-not (Test-Path -LiteralPath $extracted -PathType Leaf)) {
        Die "archive does not contain better-drive.exe at its root"
    }

    Ensure-SecureDirectory $Prefix
    $destFile = Join-Path $Prefix "better-drive.exe"
    $srcHash = (Get-FileHash -LiteralPath $extracted -Algorithm SHA256).Hash.ToLower()

    # Staged atomic replacement; retain the previous-good binary until the
    # installed digest has been read back successfully.
    $stageFile = Join-Path $Prefix (".better-drive.exe.install-" + [guid]::NewGuid() + ".tmp")
    $backupFile = $null
    Copy-Item -LiteralPath $extracted -Destination $stageFile
    $stageHash = (Get-FileHash -LiteralPath $stageFile -Algorithm SHA256).Hash.ToLower()
    if ($stageHash -ne $srcHash) {
        Remove-Item -LiteralPath $stageFile -Force -ErrorAction SilentlyContinue
        Die "staged binary hash mismatch"
    }

    if (Test-Path -LiteralPath $destFile) {
        $backupFile = Join-Path $Prefix (".better-drive.exe.old-" + [guid]::NewGuid() + ".tmp")
        try {
            [System.IO.File]::Replace($stageFile, $destFile, $backupFile)
        } catch {
            $replaceError = $_.Exception.Message
            Remove-Item -LiteralPath $stageFile -Force -ErrorAction SilentlyContinue
            if (Test-Path -LiteralPath $backupFile) {
                Remove-Item -LiteralPath $destFile -Force -ErrorAction SilentlyContinue
                [System.IO.File]::Move($backupFile, $destFile)
            }
            Die "atomic replacement failed; previous binary restored: $replaceError"
        }
    } else {
        [System.IO.File]::Move($stageFile, $destFile)
    }

    try {
        $installedHash = (Get-FileHash -LiteralPath $destFile -Algorithm SHA256).Hash.ToLower()
    } catch {
        $readbackError = $_.Exception.Message
        if ($backupFile -and (Test-Path -LiteralPath $backupFile)) {
            Remove-Item -LiteralPath $destFile -Force -ErrorAction SilentlyContinue
            [System.IO.File]::Move($backupFile, $destFile)
        } else {
            Remove-Item -LiteralPath $destFile -Force -ErrorAction SilentlyContinue
        }
        Die "installed binary readback failed; previous state restored: $readbackError"
    }
    if ($installedHash -ne $srcHash) {
        if ($backupFile -and (Test-Path -LiteralPath $backupFile)) {
            Remove-Item -LiteralPath $destFile -Force -ErrorAction SilentlyContinue
            [System.IO.File]::Move($backupFile, $destFile)
        } else {
            Remove-Item -LiteralPath $destFile -Force -ErrorAction SilentlyContinue
        }
        Die "installed binary hash mismatch; previous state restored"
    }
    if ($backupFile) {
        Remove-Item -LiteralPath $backupFile -Force -ErrorAction SilentlyContinue
    }

    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    if ($userPath -notlike "*$Prefix*") {
        [Environment]::SetEnvironmentVariable("Path", "$userPath;$Prefix", "User")
        Log "Added $Prefix to user PATH (restart shell to apply)"
    }

    $installed = & $destFile --version
    Log "Installed: $installed"
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

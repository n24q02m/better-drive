#!/usr/bin/env python3
"""Verify installer security contracts, Sigstore bundle verification, and SAFE-ARCHIVE rules."""
from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path


REQUIRED_INSTALL_PS1_TOKENS = (
    "SAFE-ARCHIVE-V1",
    "cosign is required",
    "Expand-SafeZip",
    "CreateNew",
    "Ensure-SecureDirectory",
    "Assert-NoSymlinkAncestors",
    "checksums.txt.sigstore.json",
    "--bundle",
    "--certificate-identity-regexp",
    "--certificate-oidc-issuer",
    "Replace",
)

FORBIDDEN_INSTALL_PS1_TOKENS = (
    "WARN: cosign verify failed",
    "Expand-Archive (Join-Path $tmp",
    "Copy-Item -LiteralPath $extracted -Destination (Join-Path $Prefix \"better-drive.exe\") -Force",
    "Move-Item -LiteralPath $stageFile -Destination $destFile -Force",
)

REQUIRED_INSTALL_SH_TOKENS = (
    "SAFE-ARCHIVE-V1",
    "need cosign",
    "safe_extract_tar",
    "cosign verify-blob",
    "checksums.txt.sigstore.json",
    "--bundle",
    "--certificate-identity-regexp",
    "--certificate-oidc-issuer",
    "ensure_dir_secure",
    "verify_no_symlink_ancestors",
    "compute_file_sha256",
)

FORBIDDEN_INSTALL_SH_TOKENS = (
    "skipping signature check",
    'tar -xzf "$tmp/better-drive.tar.gz"',
    'sudo install -m 0755 "$extract/better-drive" "$dest"',
    "mv -f",
)
FORBIDDEN_DETACHED_SIGNATURE_PATTERNS = (
    r"checksums\.txt\.(?:pem|sig)(?!store)",
    r"--certificate(?=\s|[`\"'])",
    r"--signature(?=\s|[`\"'])",
)


def verify_static_installer_contracts(root: Path) -> list[str]:
    findings: list[str] = []
    ps_file = root / "install.ps1"
    sh_file = root / "install.sh"

    if not ps_file.is_file():
        return ["install.ps1 not found"]
    if not sh_file.is_file():
        return ["install.sh not found"]

    ps = ps_file.read_text(encoding="utf-8")
    sh = sh_file.read_text(encoding="utf-8")

    for token in REQUIRED_INSTALL_PS1_TOKENS:
        if token not in ps:
            findings.append(f"install.ps1: missing required token '{token}'")
    for token in FORBIDDEN_INSTALL_PS1_TOKENS:
        if token in ps:
            findings.append(f"install.ps1: forbidden token found '{token}'")
    for pattern in FORBIDDEN_DETACHED_SIGNATURE_PATTERNS:
        if re.search(pattern, ps, re.IGNORECASE):
            findings.append(f"install.ps1: forbidden detached-signature pattern '{pattern}'")

    for token in REQUIRED_INSTALL_SH_TOKENS:
        if token not in sh:
            findings.append(f"install.sh: missing required token '{token}'")
    for token in FORBIDDEN_INSTALL_SH_TOKENS:
        if token in sh:
            findings.append(f"install.sh: forbidden token found '{token}'")
    for pattern in FORBIDDEN_DETACHED_SIGNATURE_PATTERNS:
        if re.search(pattern, sh, re.IGNORECASE):
            findings.append(f"install.sh: forbidden detached-signature pattern '{pattern}'")

    # Verify cosign bundle command shape, allowing shell line continuations.
    if not re.search(r"cosign\s+verify-blob(?:\s|\\\s)*--bundle", sh):
        findings.append("install.sh: cosign verify-blob must use --bundle")
    if not re.search(r"--bundle\s+\(Join-Path\s+\$tmp\s+\"checksums\.txt\.sigstore\.json\"\)", ps):
        findings.append("install.ps1: cosign verification must pass sigstore bundle")

    replace_index = ps.find("[System.IO.File]::Replace")
    installed_hash_index = ps.find("$installedHash", replace_index)
    backup_cleanup_index = ps.find("Remove-Item -LiteralPath $backupFile", replace_index)
    if replace_index < 0 or installed_hash_index < 0:
        findings.append("install.ps1: atomic replacement and installed hash readback are required")
    elif 0 <= backup_cleanup_index < installed_hash_index:
        findings.append("install.ps1: previous-good backup is deleted before installed hash readback")
    return findings


WINDOWS_DEVICE = re.compile(r"^(?:CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(?:\.|$)", re.IGNORECASE)
MAX_ENTRIES = 4
MAX_ENTRY_BYTES = 100 * 1024 * 1024
MAX_TOTAL_BYTES = 500 * 1024 * 1024


def validate_members(members: list[tuple[str, int, str]], expected_binary: str) -> str | None:
    if not members or len(members) > MAX_ENTRIES:
        return "archive entry budget violated"
    seen: set[str] = set()
    total = 0
    found_binary = False
    seen_classes: set[str] = set()
    for raw_name, size, kind in members:
        if "\x00" in raw_name:
            return "null byte"
        name = raw_name.replace("\\", "/")
        if name.startswith("/") or re.match(r"^[A-Za-z]:", name):
            return "absolute path"
        parts = [part for part in name.rstrip("/").split("/") if part]
        if not parts or any(part in {".", ".."} for part in parts):
            return "path traversal"
        if len(parts) != 1:
            return "nested or escaped path"
        leaf = parts[0]
        if WINDOWS_DEVICE.match(leaf):
            return "Windows device path"
        key = leaf.casefold()
        if key in seen:
            return "duplicate path"
        seen.add(key)
        if kind not in {"file", "directory"}:
            return f"forbidden entry type {kind}"
        if size < 0 or size > MAX_ENTRY_BYTES:
            return "per-entry budget violated"
        total += size
        if total > MAX_TOTAL_BYTES:
            return "aggregate budget violated"
        if kind == "file":
            if leaf == expected_binary:
                member_class = "binary"
                found_binary = True
            else:
                match = re.match(r"^(LICENSE|README|CHANGELOG)(?:\..*)?$", leaf, re.IGNORECASE)
                if not match:
                    return "unexpected file"
                member_class = match.group(1).upper()
            if member_class in seen_classes:
                return "duplicate member class"
            seen_classes.add(member_class)
    if not found_binary:
        return "expected binary missing"
    return None


def verify_safe_archive_matrix() -> list[str]:
    """Pressure-test the shared SAFE-ARCHIVE-V1 member policy."""
    findings: list[str] = []
    good = [( "better-drive.exe", 7, "file"), ("LICENSE", 10, "file")]
    if error := validate_members(good, "better-drive.exe"):
        findings.append(f"good archive rejected: {error}")

    bad_cases = {
        "traversal": [("../evil.exe", 1, "file"), ("better-drive.exe", 1, "file")],
        "absolute": [("/evil.exe", 1, "file"), ("better-drive.exe", 1, "file")],
        "drive": [(r"C:\evil.exe", 1, "file"), ("better-drive.exe", 1, "file")],
        "device": [("CON.txt", 1, "file"), ("better-drive.exe", 1, "file")],
        "duplicate": [("better-drive.exe", 1, "file"), ("BETTER-DRIVE.EXE", 1, "file")],
        "symlink": [("better-drive.exe", 1, "symlink")],
        "hardlink": [("better-drive.exe", 1, "hardlink")],
        "special": [("better-drive.exe", 1, "device")],
        "entry_budget": [("better-drive.exe", MAX_ENTRY_BYTES + 1, "file")],
        "aggregate_budget": [
            ("better-drive.exe", MAX_ENTRY_BYTES, "file"),
            *[(f"README.{index}", MAX_ENTRY_BYTES, "file") for index in range(5)],
        ],
        "unexpected": [("better-drive.exe", 1, "file"), ("payload.dll", 1, "file")],
    }
    for label, members in bad_cases.items():
        if validate_members(members, "better-drive.exe") is None:
            findings.append(f"malicious archive case accepted: {label}")
    return findings


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("root", type=Path)
    root = parser.parse_args().root

    findings = verify_static_installer_contracts(root)
    findings.extend(verify_safe_archive_matrix())

    if findings:
        for finding in findings:
            print(f"FAIL: {finding}")
        return 1

    print("PASS: installer SAFE-ARCHIVE-V1 and Sigstore bundle verification checks")
    return 0


if __name__ == "__main__":
    sys.exit(main())

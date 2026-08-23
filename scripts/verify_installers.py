#!/usr/bin/env python3
from __future__ import annotations

import argparse
import sys
from pathlib import Path


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("root", type=Path)
    root = parser.parse_args().root
    ps = (root / "install.ps1").read_text(encoding="utf-8")
    sh = (root / "install.sh").read_text(encoding="utf-8")
    findings: list[str] = []
    for name, text, required, forbidden in (
        ("install.ps1", ps, ("SAFE-ARCHIVE-V1", "cosign is required", "Expand-SafeZip", "CreateNew"), ("WARN: cosign verify failed", "Expand-Archive (Join-Path $tmp")),
        ("install.sh", sh, ("SAFE-ARCHIVE-V1", "need cosign", "safe_extract_tar", "cosign verify-blob"), ("skipping signature check", "tar -xzf \"$tmp/better-drive.tar.gz\"")),
    ):
        for token in required:
            if token not in text:
                findings.append(f"{name}: missing {token}")
        for token in forbidden:
            if token in text:
                findings.append(f"{name}: forbidden legacy pattern {token}")
    if findings:
        for finding in findings:
            print(f"FAIL: {finding}")
        return 1
    print("PASS: installer SAFE-ARCHIVE-V1 and fail-closed signature checks")
    return 0


if __name__ == "__main__":
    sys.exit(main())

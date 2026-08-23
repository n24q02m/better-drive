#!/usr/bin/env python3
"""Verify the credential-free GoReleaser prepare contract."""
from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

REQUIRED_SECTIONS = ("builds:", "archives:", "checksum:", "sboms:")
FORBIDDEN_PREPARE_TOKENS = (
    "signs:",
    "homebrew_casks:",
    "scoops:",
    "replace_existing_artifacts:",
    "publishers:",
    "announcements:",
)


def normalized_lines(text: str) -> list[str]:
    return [line.strip() for line in text.splitlines() if line.strip() and not line.lstrip().startswith("#")]


def section(text: str, heading: str) -> list[str]:
    out: list[str] = []
    active = False
    for raw in text.splitlines():
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue
        if raw.strip() == heading:
            active = True
        elif active and not raw.startswith((" ", "\t", "-")) and re.match(r"^[A-Za-z0-9_-]+:$", raw.strip()):
            break
        if active:
            out.append(raw.strip())
    return out


def check(root: Path) -> list[str]:
    release_path = root / ".goreleaser.yaml"
    prepare_path = root / ".goreleaser.prepare.yaml"
    release = release_path.read_text(encoding="utf-8")
    prepare = prepare_path.read_text(encoding="utf-8")
    findings: list[str] = []
    for heading in REQUIRED_SECTIONS:
        if not section(release, heading):
            findings.append(f"release missing {heading}")
        if not section(prepare, heading):
            findings.append(f"prepare missing {heading}")
        elif section(release, heading) != section(prepare, heading):
            findings.append(f"prepare drift in {heading}")
    for token in FORBIDDEN_PREPARE_TOKENS:
        if token in prepare:
            findings.append(f"prepare contains forbidden {token}")
    if 'commit_message = "fix(release): v{version} [skip ci]"' not in (root / "semantic-release.toml").read_text(encoding="utf-8"):
        findings.append("semantic release commit message is not canonical")
    return findings


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("root", type=Path)
    args = parser.parse_args()
    findings = check(args.root)
    if findings:
        for finding in findings:
            print(f"FAIL: {finding}")
        return 1
    print("PASS: credential-free prepare parity")
    return 0


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""Verify dispatch-only release and non-replacing artifact policy."""
from __future__ import annotations

import argparse
import sys
from pathlib import Path


def check(root: Path) -> list[str]:
    workflow = (root / ".github" / "workflows" / "cd.yml").read_text(encoding="utf-8")
    goreleaser = (root / ".goreleaser.yaml").read_text(encoding="utf-8")
    prepare = (root / ".goreleaser.prepare.yaml").read_text(encoding="utf-8")
    findings: list[str] = []
    on_block = workflow.split("permissions:", 1)[0]
    if "  push:" in on_block or "tags:" in on_block:
        findings.append("CD must not publish from a tag push")
    if "  workflow_dispatch:" not in on_block:
        findings.append("CD must expose workflow_dispatch")
    if "group: cd-better-drive" not in workflow:
        findings.append("CD concurrency must be repository-global")
    if "beta-publish" not in workflow or "stable-publish" not in workflow:
        findings.append("CD publisher must bind beta-publish and stable-publish environments")
    if "replace_existing_artifacts: false" not in goreleaser:
        findings.append("GoReleaser must reject replacement of existing artifacts")
    for token in ("signs:", "publishers:", "homebrew_casks:", "scoops:"):
        if token in prepare:
            findings.append(f"prepare config contains forbidden {token}")
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
    print("PASS: dispatch-only stable/beta publisher policy")
    return 0


if __name__ == "__main__":
    sys.exit(main())

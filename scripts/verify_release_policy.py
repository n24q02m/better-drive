#!/usr/bin/env python3
"""Verify dispatch-only release, credential-free candidate preparation, and non-replacing artifact policy."""
from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path


def check(root: Path) -> list[str]:
    workflow_dir = root / ".github" / "workflows"
    workflow_path = workflow_dir / "cd.yml"
    ci_path = workflow_dir / "ci.yml"
    goreleaser_path = root / ".goreleaser.yaml"
    prepare_path = root / ".goreleaser.prepare.yaml"
    if not workflow_path.is_file():
        return [".github/workflows/cd.yml not found"]
    if not goreleaser_path.is_file():
        return [".goreleaser.yaml not found"]
    if not prepare_path.is_file():
        return [".goreleaser.prepare.yaml not found"]
    if not ci_path.is_file():
        return [".github/workflows/ci.yml not found"]

    workflow = workflow_path.read_text(encoding="utf-8")
    ci = ci_path.read_text(encoding="utf-8")
    goreleaser = goreleaser_path.read_text(encoding="utf-8")
    prepare = prepare_path.read_text(encoding="utf-8")
    findings: list[str] = []

    workflow_names = sorted(path.name for path in workflow_dir.glob("*.yml"))
    if workflow_names != ["cd.yml", "ci.yml"]:
        findings.append(
            "workflow directory must contain exactly ci.yml and cd.yml; "
            f"got {workflow_names}"
        )
    for job in ("bot-governance", "scorecard"):
        if f"  {job}:" not in ci:
            findings.append(f"CI must own the consolidated {job} job")
    for trigger in ("  branch_protection_rule:", "  schedule:"):
        if trigger not in ci.split("permissions:", 1)[0]:
            findings.append(f"CI must own the consolidated {trigger.strip(': ')} trigger")
    scorecard_block = ci.split("  scorecard:", 1)[-1].split("\n  bot-governance:", 1)[0]
    if "github.ref == 'refs/heads/main'" not in scorecard_block:
        findings.append("Scorecard must run only on the default main ref")

    # 1. Trigger constraints
    on_block = workflow.split("permissions:", 1)[0]
    if "  push:" in on_block or "tags:" in on_block:
        findings.append("CD must not publish from a tag push")
    if "  workflow_dispatch:" not in on_block:
        findings.append("CD must expose workflow_dispatch")

    # 2. Concurrency constraints
    if "group: cd-better-drive" not in workflow:
        findings.append("CD concurrency must be repository-global ('group: cd-better-drive')")

    # 3. GoReleaser safety policies
    if "replace_existing_artifacts: false" not in goreleaser:
        findings.append("GoReleaser (.goreleaser.yaml) must reject replacement of existing artifacts")

    for token in ("signs:", "publishers:", "homebrew_casks:", "scoops:", "release:"):
        if token in prepare:
            findings.append(f"prepare config contains forbidden publishing token '{token}'")

    # 4. Credential-free candidate build validation
    if "--config=.goreleaser.prepare.yaml" not in workflow:
        findings.append("CD candidate build must specify --config=.goreleaser.prepare.yaml")
    if "--skip=publish" not in workflow:
        findings.append("CD candidate build must specify --skip=publish")

    # Forbidden plain release --clean without prepare config and skip=publish
    if re.search(r"args:\s+release\s+--clean(?!\s+--config)", workflow):
        findings.append("CD must not execute plain unconstrained 'release --clean'")

    # Check for forbidden secrets in candidate preparation only. The stable
    # publish job legitimately uses the CI App identity and the tap token.
    prepare_block = workflow.split("  publish-stable:", 1)[0]
    for secret in ("TAP_GITHUB_TOKEN", "CI_APP_KEY", "CI_APP_ID"):
        if secret in prepare_block:
            findings.append(f"CD prepare candidate workflow must not reference secret/var '{secret}'")

    # 5. Candidate artifact archiving
    if "actions/upload-artifact" not in workflow:
        findings.append("CD must upload candidate artifacts via actions/upload-artifact")

    # 6. Stable publication policy
    stable_block = workflow.split("  publish-stable:", 1)[-1]
    if "  publish-stable:" not in workflow:
        findings.append("CD must own the consolidated publish-stable job")
    if "environment: stable-publish" not in stable_block:
        findings.append("CD stable publication must run in the protected stable-publish environment")
    if "n24q02m/better-semantic-release@087b84e8d2ba75bdec350924d3bf8247088e0b1a" not in stable_block:
        findings.append("CD stable release must use the pinned better-semantic-release action (v1.4.0)")
    if "actions/create-github-app-token" not in stable_block:
        findings.append("CD stable release must push the release commit via the CI App identity")
    if "args: release --config=.goreleaser.yaml --clean" not in stable_block:
        findings.append("CD stable publication must build with the full pinned .goreleaser.yaml")
    if "config_file: semantic-release.toml" not in stable_block:
        findings.append("CD stable release must consume semantic-release.toml")


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
    print("PASS: dispatch-only credential-free candidate preparation and non-replacing artifact policy")
    return 0


if __name__ == "__main__":
    sys.exit(main())

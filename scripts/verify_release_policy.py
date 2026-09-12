#!/usr/bin/env python3
"""Verify the unified merge=release ladder: push staging = beta prerelease, push main = stable.

Unification contract (2026-09-11): merge = release. The staging branch auto-publishes
beta prereleases with no reviewer gate; a squash-merge into main is the only stable
user gate and auto-publishes through the protected stable-publish environment.
Candidate-bundle ceremony (prepare lane + candidate artifacts) is retired."""
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
    if not workflow_path.is_file():
        return [".github/workflows/cd.yml not found"]
    if not goreleaser_path.is_file():
        return [".goreleaser.yaml not found"]
    if not ci_path.is_file():
        return [".github/workflows/ci.yml not found"]

    workflow = workflow_path.read_text(encoding="utf-8")
    ci = ci_path.read_text(encoding="utf-8")
    goreleaser = goreleaser_path.read_text(encoding="utf-8")
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

    # 1. Ladder triggers: push staging = beta, push main = stable; tags never publish.
    on_block = workflow.split("permissions:", 1)[0]
    if "  push:" not in on_block:
        findings.append("CD must trigger on push (merge = release)")
    if "    - staging" not in on_block or "    - main" not in on_block:
        findings.append("CD push trigger must cover both staging and main")
    if "  tags:" in on_block:
        findings.append("CD must not publish from a tag push")
    if "  workflow_dispatch:" not in on_block:
        findings.append("CD must keep workflow_dispatch as the escape hatch")
    if "RELEASE_TYPE:" not in workflow:
        findings.append("CD must resolve RELEASE_TYPE from the push ref (staging=beta, main=stable)")

    # 2. Concurrency constraints
    if "group: cd-better-drive" not in workflow:
        findings.append("CD concurrency must be repository-global ('group: cd-better-drive')")

    # 3. GoReleaser safety policies
    if "replace_existing_artifacts: false" not in goreleaser:
        findings.append("GoReleaser (.goreleaser.yaml) must reject replacement of existing artifacts")
    if "prerelease: auto" not in goreleaser:
        findings.append("GoReleaser (.goreleaser.yaml) must keep prerelease: auto so beta tags never take the Latest slot")

    # 4. Publication policy: single publish path through the protected environments
    publish_marker = "  publish:"
    if publish_marker not in workflow:
        findings.append("CD must own a single consolidated publish job")
    publish_block = workflow.split(publish_marker, 1)[-1]
    if "beta-publish" not in publish_block or "stable-publish" not in publish_block:
        findings.append("CD publish job must select beta-publish/stable-publish from the resolved channel")
    if "n24q02m/better-semantic-release@6e6884898bfe1ac34a0bc64ed62f54a21562df23" not in publish_block:
        findings.append("CD publish must use the pinned better-semantic-release action (v1.6.1)")
    if "prerelease: ${{ env.RELEASE_TYPE == 'beta' }}" not in publish_block:
        findings.append("CD publish must wire the beta channel into the pinned action (prerelease + prerelease_token)")
    if "actions/create-github-app-token" not in publish_block:
        findings.append("CD publish must push the release commit via the CI App identity")
    if "args: release --config=.goreleaser.yaml --clean" not in publish_block:
        findings.append("CD publish must build with the full pinned .goreleaser.yaml")
    if "config_file: semantic-release.toml" not in publish_block:
        findings.append("CD publish must consume semantic-release.toml")

    # 5. Retired ceremony must stay retired
    if "prepare-candidate" in workflow or "publish-stable" in workflow:
        findings.append("CD must not resurrect the retired candidate-bundle jobs (prepare-candidate/publish-stable)")

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
    print("PASS: unified merge=release ladder (staging push = beta, main push = stable, no candidate ceremony)")
    return 0


if __name__ == "__main__":
    sys.exit(main())

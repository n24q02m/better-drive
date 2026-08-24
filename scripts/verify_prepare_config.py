#!/usr/bin/env python3
"""Verify the credential-free GoReleaser prepare contract, section parity, and deterministic timestamps."""
from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

REQUIRED_SECTIONS = ("builds:", "archives:", "checksum:", "sboms:", "changelog:")

FORBIDDEN_PREPARE_TOKENS = (
    "signs:",
    "homebrew_casks:",
    "scoops:",
    "replace_existing_artifacts:",
    "publishers:",
    "announcements:",
    "release:",
    "milestones:",
    "nfpms:",
    "brews:",
    "crates:",
    "dockers:",
    "snapcrafts:",
    "aur:",
    "kos:",
    "winget:",
    "blob:",
    "TAP_GITHUB_TOKEN",
    "GITHUB_TOKEN",
)


def extract_section_lines(text: str, heading: str) -> list[str]:
    out: list[str] = []
    active = False
    for raw in text.splitlines():
        line = raw.rstrip()
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        if stripped == heading and not raw.startswith((" ", "\t", "-")):
            active = True
            out.append(stripped)
            continue
        if active:
            if not raw.startswith((" ", "\t", "-")) and re.match(r"^[A-Za-z0-9_-]+:$", stripped):
                break
            out.append(stripped)
    return out


def yaml_structure_without_block_scalars(text: str) -> str:
    """Return YAML structure while omitting literal/folded scalar bodies."""
    structural: list[str] = []
    block_indent: int | None = None
    for line in text.splitlines():
        stripped = line.lstrip(" ")
        indent = len(line) - len(stripped)
        if block_indent is not None:
            if not stripped or indent > block_indent:
                continue
            block_indent = None
        structural.append(line)
        if re.search(r":\s*[>|][+-]?\s*(?:#.*)?$", line):
            block_indent = indent
    return "\n".join(structural)


def check_action_anchors_and_ids(workflow_path: Path) -> list[str]:
    findings: list[str] = []
    if not workflow_path.is_file():
        return findings

    text = workflow_path.read_text(encoding="utf-8")
    filename = workflow_path.name

    # Check actual YAML anchors, excluding shell bodies such as `>&2`.
    structural = yaml_structure_without_block_scalars(text)
    anchors = re.findall(r"(?<!>)&([A-Za-z_][A-Za-z0-9_-]*)\b", structural)
    seen_anchors: set[str] = set()
    for anchor in anchors:
        if anchor in seen_anchors:
            findings.append(f"{filename}: duplicate YAML anchor '&{anchor}'")
        seen_anchors.add(anchor)

    # Check job and step id uniqueness within each job
    job_blocks = re.split(r"\n  ([a-zA-Z0-9_-]+):\n", text)
    if len(job_blocks) > 1:
        # job_blocks[1::2] are job names, job_blocks[2::2] are job bodies
        for job_name, job_body in zip(job_blocks[1::2], job_blocks[2::2]):
            step_ids = re.findall(r"^\s+id:\s*([a-zA-Z0-9_-]+)", job_body, re.MULTILINE)
            seen_steps: set[str] = set()
            for step_id in step_ids:
                if step_id in seen_steps:
                    findings.append(f"{filename} (job {job_name}): duplicate step id '{step_id}'")
                seen_steps.add(step_id)

    return findings


def check(root: Path) -> list[str]:
    release_path = root / ".goreleaser.yaml"
    prepare_path = root / ".goreleaser.prepare.yaml"
    semantic_path = root / "semantic-release.toml"

    if not release_path.is_file():
        return [".goreleaser.yaml not found"]
    if not prepare_path.is_file():
        return [".goreleaser.prepare.yaml not found"]
    if not semantic_path.is_file():
        return ["semantic-release.toml not found"]

    release = release_path.read_text(encoding="utf-8")
    prepare = prepare_path.read_text(encoding="utf-8")
    semantic = semantic_path.read_text(encoding="utf-8")
    findings: list[str] = []

    # Check section parity for shared GoReleaser sections
    for heading in REQUIRED_SECTIONS:
        rel_sec = extract_section_lines(release, heading)
        prep_sec = extract_section_lines(prepare, heading)
        if not rel_sec:
            findings.append(f"release (.goreleaser.yaml) missing required section {heading}")
        if not prep_sec:
            findings.append(f"prepare (.goreleaser.prepare.yaml) missing required section {heading}")
        elif rel_sec != prep_sec:
            findings.append(f"prepare drift detected in section {heading}")

    # Check forbidden tokens in prepare config
    for token in FORBIDDEN_PREPARE_TOKENS:
        if token in prepare:
            findings.append(f"prepare config contains forbidden token '{token}'")

    # Check deterministic build rules
    if 'mod_timestamp: "{{ .CommitTimestamp }}"' not in release:
        findings.append("release (.goreleaser.yaml) must specify deterministic mod_timestamp: '{{ .CommitTimestamp }}'")
    if 'mod_timestamp: "{{ .CommitTimestamp }}"' not in prepare:
        findings.append("prepare (.goreleaser.prepare.yaml) must specify deterministic mod_timestamp: '{{ .CommitTimestamp }}'")

    if "Date={{.CommitTimestamp}}" not in release:
        findings.append("release (.goreleaser.yaml) version.Date must use deterministic {{.CommitTimestamp}}")
    if "Date={{.CommitTimestamp}}" not in prepare:
        findings.append("prepare (.goreleaser.prepare.yaml) version.Date must use deterministic {{.CommitTimestamp}}")

    if "{{.Date}}" in release or "{{.Date}}" in prepare:
        findings.append("non-deterministic {{.Date}} token found (must use {{.CommitTimestamp}})")

    # Check workflow anchor / id uniqueness
    workflows_dir = root / ".github" / "workflows"
    if workflows_dir.is_dir():
        for wf in sorted(workflows_dir.glob("*.yml")):
            findings.extend(check_action_anchors_and_ids(wf))

    # Check canonical semantic release commit message
    if 'commit_message = "fix(release): v{version} [skip ci]"' not in semantic:
        findings.append("semantic release commit message is not canonical ('fix(release): v{version} [skip ci]')")

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
    print("PASS: credential-free prepare parity and deterministic release rules")
    return 0


if __name__ == "__main__":
    sys.exit(main())

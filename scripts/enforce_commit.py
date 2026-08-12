"""Validate the repository's feat/fix commit subject convention."""

from __future__ import annotations

import re
import sys
from pathlib import Path


COMMIT_SUBJECT = re.compile(r"^(?:feat|fix)(?:\([^\n()]+\))?:\s+\S.*$")
BREAKING_MARKER = re.compile(r"^(?:feat|fix)(?:\([^\n()]+\))?!:")


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: enforce_commit.py <commit-message-file>", file=sys.stderr)
        return 2

    message = Path(sys.argv[1]).read_text(encoding="utf-8")
    lines = message.splitlines()
    subject = lines[0].strip() if lines else ""
    if not COMMIT_SUBJECT.fullmatch(subject) or BREAKING_MARKER.match(subject):
        print(
            "Commit subject must use feat: or fix: (scoped forms are allowed) "
            "without a breaking-change marker.",
            file=sys.stderr,
        )
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

"""Fail when a Go file is not formatted, without rewriting the worktree."""

from __future__ import annotations

import subprocess
import sys


def main() -> int:
    files = sys.argv[1:]
    if not files:
        listed = subprocess.run(
            ["git", "ls-files", "--", "*.go"],
            check=False,
            text=True,
            capture_output=True,
        )
        if listed.returncode != 0:
            print(listed.stderr, end="", file=sys.stderr)
            return listed.returncode
        files = listed.stdout.splitlines()
    if not files:
        return 0

    result = subprocess.run(
        ["gofmt", "-l", *files],
        check=False,
        text=True,
        capture_output=True,
    )
    if result.returncode != 0:
        print(result.stderr, end="", file=sys.stderr)
        return result.returncode
    if result.stdout:
        print("Unformatted Go files:")
        print(result.stdout, end="")
        print("Run gofmt -w on the listed files.", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

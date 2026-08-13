# better-drive

[![CI](https://github.com/n24q02m/better-drive/actions/workflows/ci.yml/badge.svg)](https://github.com/n24q02m/better-drive/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue.svg)](LICENSE)

<!-- BEGIN: AUTO-GENERATED-CROSS-PROMO -->
<details>
  <summary><strong>Sister projects from n24q02m</strong> (click to expand)</summary>

| Project | Tagline | Tag |
|---|---|---|
| [agent-chat-plugin](https://github.com/n24q02m/agent-chat-plugin) | Peer AI agents chat in a shared folder — no human relay, no orchestrator, wor... | Tooling |
| [better-code-review-graph](https://github.com/n24q02m/better-code-review-graph) | Knowledge graph for token-efficient code reviews -- semantic search and call-... | MCP |
| [better-drive](https://github.com/n24q02m/better-drive) | 2-way Google Drive sync with .driveignore filter — rclone engine, Windows tray | Tooling |
| [better-email-mcp](https://github.com/n24q02m/better-email-mcp) | IMAP/SMTP email for AI agents -- read, send, organize folders, and manage att... | MCP |
| [better-godot-mcp](https://github.com/n24q02m/better-godot-mcp) | Composite MCP server for Godot Engine -- 17 composite tools for AI-assisted g... | MCP |
| [better-notion-mcp](https://github.com/n24q02m/better-notion-mcp) | Markdown-first Notion for AI agents -- pages, databases, blocks, and comments... | MCP |
| [better-semantic-release](https://github.com/n24q02m/better-semantic-release) | Drop-in python-semantic-release fork with built-in release-safety guards (orp... | Tooling |
| [better-telegram-mcp](https://github.com/n24q02m/better-telegram-mcp) | Telegram for AI agents -- messages, chats, media, and contacts across both bo... | MCP |
| [better-workspace-mcp](https://github.com/n24q02m/better-workspace-mcp) | Google Workspace MCP server (Docs/Drive/Calendar/Gmail/Sheets/Slides/Tasks/Ch... | MCP |
| [claude-plugins](https://github.com/n24q02m/claude-plugins) | Claude Code plugin marketplace for the n24q02m MCP servers -- install web sea... | Marketplace |
| [imagine-mcp](https://github.com/n24q02m/imagine-mcp) | Image and video understanding + generation for AI agents -- across Gemini, Op... | MCP |
| [jules-task-archiver](https://github.com/n24q02m/jules-task-archiver) | Chrome Extension for bulk operations on Jules tasks via batchexecute API -- a... | Tooling |
| [mcp-core](https://github.com/n24q02m/mcp-core) | Shared foundation for building MCP servers -- Streamable HTTP transport, OAut... | MCP |
| [mnemo-mcp](https://github.com/n24q02m/mnemo-mcp) | Persistent AI memory with hybrid search and embedded sync. Open, free, unlimi... | MCP |
| [qwen3-embed](https://github.com/n24q02m/qwen3-embed) | Lightweight Qwen3 text embedding and reranking via ONNX Runtime and GGUF | Library |
| [skret](https://github.com/n24q02m/skret) | Secrets without the server. | CLI |
| [tacet](https://github.com/n24q02m/tacet) | A self-distilling neuro-symbolic cascade that amortises LLM cost across knowl... | Tooling |
| [web-core](https://github.com/n24q02m/web-core) | Shared web infrastructure package for search, scraping, HTTP security, and st... | Library |
| [wet-mcp](https://github.com/n24q02m/wet-mcp) | Open-source MCP server for AI agents: web search, content extraction, and lib... | MCP |

</details>
<!-- END: AUTO-GENERATED-CROSS-PROMO -->


Cross-platform Google Drive sync and virtual-drive mount — bisync (2-way), copy, sync (1-way mirror), or a foreground mounted filesystem. A thin, lean wrapper around the [rclone](https://rclone.org) binary: better-drive owns the ergonomics (`.driveignore`, multi-pair config, a system-tray daemon, per-OS autostart, and a safe mount contract) while your installed `rclone` performs transfers and filesystem mounting.

Runs on Windows, Linux, and macOS. The standalone binary requires `rclone` on `PATH` (installed automatically by the scoop/brew packages below).

## Contents

- [Install](#install)
- [Quick start](#quick-start)
- [Mount a virtual drive](#mount-a-virtual-drive)
- [Accounts](#accounts)
- [Configuration](#configuration)
- [.driveignore + config excludes](#driveignore--config-excludes)
- [How it works](#how-it-works)
- [Command reference](#command-reference)
- [Requirements](#requirements)
- [Non-interactive setup](#non-interactive-setup)
- [License](#license)

## Install

```powershell
# Windows (scoop) — pulls rclone as a dependency
scoop bucket add n24q02m https://github.com/n24q02m/scoop-bucket
scoop install better-drive
```

```bash
# macOS / Linux (Homebrew) — pulls rclone as a dependency
brew install n24q02m/homebrew-tap/better-drive

# Installed before 1.5.1? The tap now ships a cask instead of a formula
# (goreleaser retired the formula generator). Reinstall once to switch:
#   brew uninstall better-drive && brew install n24q02m/homebrew-tap/better-drive

# or one-shot installer (Linux/macOS)
curl -fsSL https://raw.githubusercontent.com/n24q02m/better-drive/main/install.sh | sh

# or from source (needs Go + rclone on PATH)
go install github.com/n24q02m/better-drive@latest
```

## Quick start

```bash
better-drive setup      # create the rclone Google Drive remote (opens browser OAuth) — or reuse an existing rclone remote
better-drive mount gdrive: G: # mount Drive in the foreground; Ctrl+C unmounts it
better-drive install    # register the daemon to start at login (HKCU Run key / LaunchAgent / systemd-user)
better-drive run        # start the sync daemon (system-tray on Windows/Linux/macOS)
better-drive status     # show configured pairs and their state
better-drive sync       # one-shot sync of every pair (for scripts/cron), then exit
better-drive uninstall  # remove the login autostart
```

The daemon syncs each pair once on start, then again every `interval`, and logs each cycle to `<config-dir>/better-drive.log`.

## Mount a virtual drive

`better-drive mount` exposes an already-configured rclone remote as a foreground filesystem. It is independent of sync pairs: `config.toml` may be absent or contain only an optional `rclone_config` path. The command verifies the remote's OAuth token before mounting, always uses `--vfs-cache-mode full` for application compatibility, streams rclone output live, and unmounts when you press Ctrl+C.

```powershell
# Windows drive letter
scoop install winfsp
better-drive mount gdrive: G:

# Let rclone choose an unused drive letter; prevent writes
better-drive mount gdrive:Documents * --read-only
```

```bash
# Linux directory mount (install FUSE 3 and ensure the user can access /dev/fuse)
mkdir -p ~/Drive
better-drive mount gdrive: ~/Drive
```

Mount requirements are OS-specific because better-drive deliberately does not embed a filesystem driver:

- Windows: [WinFsp](https://winfsp.dev/) (`scoop install winfsp`).
- Linux: FUSE 3 (`fusermount3` or `fusermount`) and permission to access `/dev/fuse`.
- macOS: an [rclone-supported mount backend](https://rclone.org/commands/rclone_mount/) such as NFS, FUSE-T, or macFUSE, depending on the installed rclone version.

The first argument must be `<remote>:<path>`; the second is passed through to rclone, so values such as `G:`, `*`, and a Unix directory retain their native meaning. Mount failures keep rclone's stderr; missing-driver failures also include an OS-specific remediation.

## Accounts

Each Google Drive account is an rclone remote; a `[[pair]]` block points its `remote` at one of them by name, so any number of accounts can be synced side by side by one `better-drive run` daemon.

```bash
better-drive account list                 # table: each account's state and pair count, offline, no network call
better-drive account list --quota         # same, plus each configured account's Drive storage usage (one rclone call per account)
better-drive account list --format json   # same data as JSON
better-drive account add --remote NAME    # same command as `better-drive setup`, under the account group
better-drive account remove NAME          # delete an account's rclone remote
```

```
$ better-drive account list
account testdrive: ready, 1 pair(s)
account testdrive2: not set up, 0 pair(s)

$ better-drive account list --format json
[
  {
    "name": "testdrive",
    "configured": true,
    "pairs": [
      "C:/Users/you/Documents"
    ]
  }
]
```

`account remove NAME` refuses while a `[[pair]]` still references the account — the error names the pairs holding it — unless `--force` is passed. `account add` is the exact same command as `setup`, reachable under either name.

### Multiple accounts

Point different `[[pair]]` blocks at different remotes to sync more than one Google account from a single daemon:

```toml
[[pair]]
local = "C:\\Users\\YourName\\GoogleDrive"
remote = "gdrive:/"
interval = "30s"

[[pair]]
local = "C:\\Users\\YourName\\WorkDrive"
remote = "gdrive-work:Backups"
interval = "6h"
```

Both pairs run concurrently under the same `better-drive run` process; `better-drive account list` reports both `gdrive` and `gdrive-work`.

## Configuration

Edit the config at your OS config dir (`%APPDATA%\better-drive\config.toml` on Windows, `~/.config/better-drive/config.toml` on Linux/macOS). Multiple `[[pair]]` blocks are supported — each is an independent sync with its own mode, interval, and excludes, all running concurrently under one `better-drive run` process:

```toml
# Optional: point at a specific rclone.conf. If omitted, better-drive auto-detects
# (scoop portable config, then the standard rclone config location).
# rclone_config = "C:\\Users\\YourName\\scoop\\apps\\rclone\\current\\rclone.conf"

[[pair]]
local = "C:\\Users\\YourName\\GoogleDrive"
remote = "MyGoogleDrive:/"
interval = "30s"

[[pair]]
local = "C:\\Users\\YourName\\Documents"
remote = "gdrive:Backups/documents"
interval = "6h"
mode = "copy"
exclude = ["node_modules/", ".venv/", "__pycache__/", ".git/"]
```

- `local`: local folder path to sync
- `remote`: rclone remote reference (format: `<remote>:<path>`)
- `interval`: check interval for this pair (e.g. "30s", "5m", "6h")
- `mode`: `bisync` (default) | `copy` | `sync` — see below
- `exclude`: optional list of gitignore-syntax patterns, evaluated as part of this pair's filters (see `.driveignore` below). Use this to exclude paths from a real, already-existing directory (e.g. a home dir) without ever writing a `.driveignore` file into it.

### Modes

- `bisync` (default): 2-way sync, deletions propagate both directions. Needs a `--resync` on first run (handled automatically) and keeps a baseline in the workdir.
- `copy`: 1-way, local -> remote. Nothing on remote is ever deleted (safe backup semantics — mirrors a no-delete `rclone copy` cron).
- `sync`: 1-way, remote is made to exactly mirror local, including deleting anything on remote not present locally.

Each pair's bisync baseline lives in its own workdir subdirectory, keyed by that pair's local and remote identity — reordering, adding, or removing `[[pair]]` blocks never disturbs another pair's baseline. If a bisync pair's baseline is missing or unusable, `sync` reports it as failed and names the fix: `better-drive sync --resync` rebuilds the baseline for every bisync-mode pair. This is a recovery command, not something to run routinely — a resync does not propagate deletions, so any file deleted on one side since the baseline broke reappears from the other side. Combine it with `--dry-run` to preview the rebuild first.

## .driveignore + config excludes

Two ways to filter what a pair syncs, and they combine (gitignore syntax, both optional):

1. **`.driveignore` file** at the root of the pair's local folder — good for filters that belong with the folder itself.
2. **`exclude` config key** on the `[[pair]]` block — good for folders you don't want to drop a hidden ignore file into, or for backup-style pairs where the filters belong with the sync config, not the source directory.

```
# Ignore system files
.DS_Store
Thumbs.db

# Ignore entire directories
node_modules/
__pycache__/

# Ignore file patterns
*.log
*.tmp

# Negate pattern (force-include)
!important.log
```

Rules are evaluated top-to-bottom, gitignore-style (config `exclude` entries first, then `.driveignore` file lines); negation patterns (`!`) override earlier ignore rules, including ones from the other source. See gitignore documentation for full pattern syntax.

## How it works

better-drive builds an `rclone` command line from each pair's config and runs the system `rclone` binary (`rclone bisync`/`copy`/`sync`), translating `.driveignore`/`exclude` rules into an rclone filter file and applying safe defaults (`--fast-list`, tuned `--transfers`/`--checkers`/`--tpslimit`, `--retries`, `--local-no-check-updated` for live directories, `--drive-skip-gdocs`). Because rclone does the transfers, better-drive stays tiny and inherits rclone's config, auth, and reliability.

`better-drive run` is a long-lived process that starts one `syncloop` (with its own ticker) per configured pair. On Windows, Linux and macOS it shows a system-tray icon with one combined status across all pairs ("Sync now" / "Pause" act on every pair at once); on any other platform it runs headless (use the log + `better-drive status`).

`better-drive mount` instead runs one foreground `rclone mount <remote:path> <mountpoint> --vfs-cache-mode full` process. It does not take the sync mutex and therefore does not block independent sync cycles. `--read-only` appends rclone's write-protection flag; Ctrl+C cancels the child process and unmounts the filesystem.

## Command reference

| Command | Purpose |
|---|---|
| `better-drive setup [flags]` | Create or repair a Google Drive rclone remote through OAuth. |
| `better-drive account list\|add\|remove` | Inspect, create, or remove Drive accounts/remotes. |
| `better-drive run` | Run every configured sync pair continuously with tray status. |
| `better-drive status [--format table\|json]` | Print configured pairs without touching the network. |
| `better-drive sync [--dry-run] [--resync] [--format table\|json]` | Run one cycle for every pair and exit. |
| `better-drive mount <remote:path> <mountpoint> [--read-only]` | Mount one remote in the foreground using VFS full-cache mode; Ctrl+C unmounts it. |
| `better-drive install` | Register the sync daemon to start at login. |
| `better-drive uninstall` | Remove the login-autostart registration. |

Run `better-drive <command> --help` for complete flags, examples, and prerequisites.

## Requirements

- [`rclone`](https://rclone.org) on `PATH` (installed automatically by the scoop and brew packages).
- A configured rclone Google Drive remote — run `better-drive setup`, or reuse a remote you already have (`rclone config`). Tip: create your own Google [client_id](https://rclone.org/drive/#making-your-own-client-id) to avoid the shared-client rate limits (pass it to `setup`/`account add` as `--client-id`/`--client-secret`).
- Mount mode additionally needs WinFsp on Windows, FUSE 3 plus `/dev/fuse` access on Linux, or a mount backend supported by the installed rclone on macOS. Sync-only commands do not need these filesystem drivers.

### Non-interactive setup

`setup` and `account add` both take `--token`, `--client-id`, `--client-secret`, `--service-account-file` and `--non-interactive`, for a machine with no browser (CI, a headless server, a remote agent). Run `rclone authorize "drive"` on a machine that does have one, then pass the printed token:

```bash
better-drive account add --remote gdrive --token '<token>' --non-interactive
# or, for a service account: --service-account-file /path/to/service-account.json
```

The token is a credential — in CI, pull it from a secret store or an environment variable rather than shell history. `--non-interactive` refuses to run without `--token` or `--service-account-file`, before touching rclone:

```
$ better-drive account add --remote probe --non-interactive
error: --non-interactive needs --token or --service-account-file; get a token by running 'rclone authorize "drive"' on a machine with a browser
```

(exit code 2, nothing created.)

## License

Apache-2.0.

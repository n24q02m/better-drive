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


Cross-platform Google Drive sync and virtual-drive mount — copy, sync, explicitly
enrolled bisync, or a foreground mounted filesystem. A thin wrapper around the
[rclone](https://rclone.org) binary: better-drive owns the job schema,
`.driveignore`, system-tray daemon, per-OS autostart, and mount contract while
rclone performs transfers and filesystem mounting.

Runs on Windows, Linux, and macOS. Sync/account jobs require an enrolled
absolute `rclone_runtime`; mount remains a separate foreground compatibility path.

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
better-drive status     # show configured jobs and their state
better-drive sync       # one-shot sync of every job (for scripts/cron), then exit
better-drive uninstall  # remove the login autostart
```

The daemon runs each job once on start, then again every `schedule`, and logs each cycle to `<config-dir>/better-drive.log`.

## Mount a virtual drive

`better-drive mount` exposes an already-configured rclone remote as a foreground filesystem. It is independent of sync jobs: `config.toml` may be absent or contain only an optional `rclone_config` path. The command verifies that the Drive remote has an OAuth token or service-account file before mounting, always uses `--vfs-cache-mode full` for application compatibility, streams rclone output live, and unmounts when you press Ctrl+C.

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

Each Google Drive account is an enrolled rclone remote referenced by a v2
`[[job.destination]]` through `credential_ref = "rclone:<remote>"`. Jobs, not
legacy `[[pair]]` blocks, are the runtime source of truth.

```bash
better-drive account list                 # account state and job count
better-drive account list --quota         # also query Drive quota
better-drive account list --format json   # machine-readable output
better-drive account add --remote NAME    # same command as `better-drive setup`
better-drive account remove NAME          # delete an account's rclone remote
```

`account remove NAME` refuses while a `[[job.destination]]` still references the
remote unless `--force` is passed. Account commands also require the pinned
`rclone_runtime`; they never fall back to PATH or ambient `RCLONE_*` discovery.

### Multiple accounts

Point destinations at different enrolled remotes to use more than one Google
account. The destination path is not provider authority; `account_id` and
`root_id` are persisted identity fields and must be enrolled before execution.

## Configuration

Edit the config at your OS config dir (`%APPDATA%\better-drive\config.toml` on
Windows, `~/.config/better-drive/config.toml` on Linux/macOS). Runtime sync
configuration is schema v2; legacy `[[pair]]` blocks are accepted only by the
read-only migration preview.

```toml
schema_version = 2

[rclone_runtime]
executable = "C:\\Program Files\\rclone\\rclone.exe"
executable_file_id = "enrolled-file-id"
executable_digest = "sha256:..."
version = "1.67.0"
provenance = "approved-release"
signature = "signature-ref"
owner = "home-role"
acl = "owner-only"
config = "C:\\Users\\YourName\\AppData\\Roaming\\rclone\\rclone.conf"
config_file_id = "enrolled-config-id"
config_digest = "sha256:..."
allowed_remotes = ["gdrive"]
allowed_backends = ["drive"]
environment = { RCLONE_LOCAL_NO_CHECK_UPDATED = "true" }

[[job]]
id = "home-claude"
source = "C:\\Users\\YourName\\.claude"
direction = "push"
mode = "copy"
required = true
category_policy_id = "claude-state"
category_policy_version = 1
category_policy_digest = "sha256:..."
symlink_policy = "preserve"
schedule = "6h"
exclude = ["node_modules/", ".venv/", "__pycache__/", ".git/"]

[[job.destination]]
backend = "drive"
path = "Backups/home/Claude"
account_id = "google-account-id"
root_id = "drive-root-id"
credential_ref = "rclone:gdrive"
required = true
retention = "30d"
min_complete_restore_sets = 2
delete_policy = "none"
```

- `id` is the stable job identity used for logs, status and bisync state.
- `direction` is `push`, `pull`, or `bidirectional`; Task 1 scheduled execution
  accepts only explicit `push`.
- `mode` is exactly `copy`, `sync`, or `bisync`. The safe migration/default is
  `copy + push`; `bisync + bidirectional` requires explicit enrollment.
- `required` applies to the source. Missing required sources fail; optional
  sources are reported as `skipped_optional`.
- `sync` additionally requires `mode_gate_ref` and `mode_gate_digest` bound to a
  real Drive E2E gate; without that evidence it fails closed.
- Every destination explicitly declares `required`, `min_complete_restore_sets
  >= 2`, and `delete_policy` (`none` or `quarantine`).
- `rclone_runtime` is pinned. Sync/account operations reject relative paths,
  missing identity/digest/provenance/ACL, PATH/default discovery, ambient
  `RCLONE_*`, and command hooks before endpoint or child-process access.

Preview legacy migration without writing:

```bash
better-drive config migrate --dry-run --format json
```

The preview redacts user path segments and materializes deterministic job IDs.
It maps missing/default/copy to `copy + push`, sync to `sync + push`, and rejects
legacy bisync unless the stable job ID is explicitly enrolled.

### Modes

- `copy + push`: one-way local-to-remote backup; remote content is never deleted.
- `sync + push`: one-way mirror; scheduled use remains behind its real Drive E2E gate.
- `bisync + bidirectional`: two-way transfer only for an explicitly enrolled,
  non-default profile; its baseline is keyed by the stable job ID.

## .driveignore + config excludes

Two ways to filter what a job syncs, and they combine (gitignore syntax, both optional):

1. **`.driveignore` file** at the root of the job's local source — good for filters that belong with the source itself.
2. **`exclude` config key** on the `[[job]]` block — good for filters that belong with the sync configuration.

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

better-drive builds an `rclone` command line from each normalized job and runs
the pinned executable/config from `rclone_runtime`, translating `.driveignore`
and `exclude` rules into an rclone filter file. Sync/account operations use an
allowlisted child environment and never inherit PATH, default config discovery,
ambient `RCLONE_*`, or hooks.

`better-drive run` starts one `syncloop` per configured job. Each bisync baseline
is keyed by the stable job ID, so reordering config blocks cannot transfer one
job's state to another. `better-drive mount` remains a separate foreground
compatibility path; it does not participate in scheduled job/runtime guarantees.

## Command reference

| Command | Purpose |
|---|---|
| `better-drive config migrate --dry-run --format json` | Preview deterministic, redacted v1-to-v2 migration without writing. |
| `better-drive setup [flags]` | Create or repair a Google Drive rclone remote through OAuth. |
| `better-drive account list\|add\|remove` | Inspect, create, or remove Drive accounts/remotes. |
| `better-drive run` | Run every configured job continuously with tray status. |
| `better-drive status [--format table\|json]` | Print configured jobs without touching the network. |
| `better-drive sync [--dry-run] [--resync] [--format table\|json]` | Run one cycle for every job and exit. |
| `better-drive restore plan|fetch|apply` | Plan and stage no-overwrite restores; live apply remains owner-gated. |
| `better-drive schedule install|status|remove` | Render/read back managed scheduler definitions without replacing an unknown owner. |
| `better-drive cleanup inventory` | Recursively capture and join every declared Drive root/page/object into read-only aggregate evidence, using a refresh-capable OAuth credential from an inherited descriptor. |
| `better-drive cleanup validate --manifest <path> [--inventory <path>]` | Validate exact IDs, safe classes, ownership/restore evidence, expiry, budgets, and optional exact inventory metadata binding. |
| `better-drive cleanup apply --manifest <path> [--journal <path>]` | Preview an exact manifest and append a hash-chain journal; `--execute` fails closed until the owner-risk broker capability is bound. |
| `better-drive cleanup approval prepare|canonicalize|activate` | Create a create-only draft, render canonical bytes for offline signing, and activate a detached signature only against an enrolled trust root and named capability. |
| `better-drive mount <remote:path> <mountpoint> [--read-only]` | Mount one remote in the foreground using VFS full-cache mode; Ctrl+C unmounts it. |
| `better-drive install` | Register the sync daemon to start at login. |
| `better-drive uninstall` | Remove the login-autostart registration. |

Run `better-drive <command> --help` for complete flags, examples, and prerequisites.

Drive-backed cleanup inventory, claimed quarantine, and fixture lifecycle
commands prefer a strict OAuth credential envelope through
`BETTER_DRIVE_DRIVE_OAUTH_CREDENTIAL_FD`. The environment carries only the
inherited descriptor number, never credential values. The legacy
`BETTER_DRIVE_DRIVE_TOKEN_FD` raw-access-token descriptor remains supported as
a non-refreshing compatibility path; supplying both descriptors is rejected.
Refresh failures are redacted and no provider mutation is retried.

## Requirements

- A pinned schema-v2 `rclone_runtime` with absolute executable/config paths,
  file identity, digest, version, provenance, signature, owner/ACL, allowed
  remotes/backends, and no ambient PATH/default discovery.
- A configured rclone Google Drive remote enrolled by `credential_ref`; run
  `better-drive setup` or reuse a remote you already have.
- Mount mode additionally needs WinFsp on Windows, FUSE 3 plus `/dev/fuse`
  access on Linux, or a supported mount backend on macOS. Sync-only commands
  do not need filesystem drivers.

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

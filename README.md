# better-drive

Cross-platform Google Drive sync — bisync (2-way), copy, or sync (1-way mirror) per pair, with `.driveignore` filters and config-level excludes. A thin, lean wrapper around the [rclone](https://rclone.org) binary: better-drive owns the ergonomics (`.driveignore`, multi-pair config, a system-tray daemon, per-OS autostart) and shells out to your installed `rclone` for the actual transfers. Supports multiple independent `[[pair]]` blocks in one config (e.g. syncing/backing up several unrelated folders under one daemon).

Runs on Windows, Linux, and macOS. The binary is small (~4 MB) and requires `rclone` on `PATH` (installed automatically by the scoop/brew packages below).

## Install

```powershell
# Windows (scoop) — pulls rclone as a dependency
scoop bucket add n24q02m https://github.com/n24q02m/scoop-bucket
scoop install better-drive
```

```bash
# macOS / Linux (Homebrew) — pulls rclone as a dependency
brew install n24q02m/homebrew-tap/better-drive

# or one-shot installer (Linux/macOS)
curl -fsSL https://raw.githubusercontent.com/n24q02m/better-drive/main/install.sh | sh

# or from source (needs Go + rclone on PATH)
go install github.com/n24q02m/better-drive@latest
```

## Quick start

```bash
better-drive setup      # create the rclone Google Drive remote (opens browser OAuth) — or reuse an existing rclone remote
better-drive install    # register the daemon to start at login (HKCU Run key / LaunchAgent / systemd-user)
better-drive run        # start the sync daemon (system-tray on Windows/Linux, headless elsewhere)
better-drive status     # show configured pairs and their state
better-drive sync       # one-shot sync of every pair (for scripts/cron), then exit
better-drive uninstall  # remove the login autostart
```

The daemon syncs each pair once on start, then again every `interval`, and logs each cycle to `<config-dir>/better-drive.log`.

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

`better-drive run` is a long-lived process that starts one `syncloop` (with its own ticker) per configured pair. On Windows and Linux it shows a system-tray icon with one combined status across all pairs ("Sync now" / "Pause" act on every pair at once); on other platforms it runs headless (use the log + `better-drive status`).

## Requirements

- [`rclone`](https://rclone.org) on `PATH` (installed automatically by the scoop and brew packages).
- A configured rclone Google Drive remote — run `better-drive setup`, or reuse a remote you already have (`rclone config`). Tip: create your own Google [client_id](https://rclone.org/drive/#making-your-own-client-id) to avoid the shared-client rate limits.

## License

MIT.

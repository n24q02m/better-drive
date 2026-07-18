# better-drive

Google Drive sync — bisync (2-way), copy, or sync (1-way mirror) per pair, with `.driveignore` filters and config-level excludes — rclone engine embedded in-process via librclone, Windows tray client. Supports multiple independent `[[pair]]` blocks in one config (e.g. syncing/backing up several unrelated folders under one daemon + tray icon).

## Khác gì upstream

| vs | better-drive thêm |
|----|-------------------|
| rclone bisync/copy/sync cron | system tray + ergonomics `.driveignore`/config excludes + multi-pair + (v2) realtime watcher |
| Google Drive for Desktop | ignore patterns + file-level control; (v2+) Linux |
| Insync | free + open source (MIT) |

## Setup

```bash
# Initialize Google Drive remote via OAuth
better-drive setup

# Start daemon + system tray
better-drive run

# Check sync status
better-drive status
```

## Configuration

Edit `%APPDATA%/better-drive/config.toml`. Multiple `[[pair]]` blocks are supported — each is an independent sync with its own mode, interval, and excludes, all running concurrently under one `better-drive run` process:

```toml
[[pair]]
local = "C:\\Users\\YourName\\GoogleDrive"
remote = "MyGoogleDrive:/"
interval = "30s"

[[pair]]
local = "C:\\Users\\YourName\\.claude"
remote = "gdrive:Backups/claude-backups/pc-1/dot-claude"
interval = "5m"
mode = "copy"
exclude = ["node_modules/", ".venv/", "__pycache__/", ".git/"]
```

- `local`: local folder path to sync
- `remote`: rclone remote reference (format: `<remote>:<path>`)
- `interval`: check interval for this pair (e.g. "30s", "5m", "1h")
- `mode`: `bisync` (default) | `copy` | `sync` — see below
- `exclude`: optional list of gitignore-syntax patterns, evaluated as part of this pair's filters (see `.driveignore` below). Use this to exclude paths from a real, already-existing directory (e.g. `~/.claude`) without ever writing a `.driveignore` file into it.

### Modes

- `bisync` (default): 2-way sync, deletions propagate both directions. Needs a `--resync` on first run (handled automatically) and keeps a baseline in the workdir.
- `copy`: 1-way, local -> remote. Nothing on remote is ever deleted (safe backup semantics — mirrors a no-delete `rclone copy` cron).
- `sync`: 1-way, remote is made to exactly mirror local, including deleting anything on remote not present locally.

## .driveignore + config excludes

Two ways to filter what a pair syncs, and they combine (gitignore syntax, both optional):

1. **`.driveignore` file** at the root of the pair's local folder — good for filters that belong with the folder itself.
2. **`exclude` config key** on the `[[pair]]` block — good for folders you don't want to touch (drop a hidden ignore file into), or for backup-style pairs where the filters belong with the sync config, not the source directory.

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

## Engine

**Engine**: rclone (MIT), embedded in-process via librclone. Single binary — no separate rclone installation needed. All sync logic runs in-memory. `better-drive run` is a long-lived foreground process with a system tray icon; it starts one `syncloop` (with its own Go ticker) per configured pair, each triggering its pair's mode (bisync/copy/sync) at that pair's interval for as long as the process keeps running. The tray shows one combined status across all pairs (syncing if any pair is syncing, else error/needs-resync if any pair is in that state, else idle/paused); "Sync now" and "Pause"/"Resume" act on every pair at once.

## Notes

`better-drive setup` runs rclone's interactive config flow in-process to create the Drive remote (this may prompt on stdin for a few fields), then opens your browser for the Google OAuth consent screen. Complete the consent flow in the browser to finish setup.

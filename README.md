# better-drive

2-way Google Drive sync with `.driveignore` filter — rclone engine embedded in-process via librclone, Windows tray client.

## Khác gì upstream

| vs | better-drive thêm |
|----|-------------------|
| rclone bisync cron | system tray + ergonomics `.driveignore` + (v2) realtime watcher |
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

Edit `%APPDATA%/better-drive/config.toml`:

```toml
[[pair]]
local = "C:\\Users\\YourName\\GoogleDrive"
remote = "MyGoogleDrive:/"
interval = "30s"
```

- `local`: local folder path to sync
- `remote`: rclone remote reference (format: `<remote>:<path>`)
- `interval`: check interval for bisync (e.g. "30s", "5m", "1h")

One config file supports one `[[pair]]` block. Multiple syncs can be added in future versions.

## .driveignore

Place `.driveignore` at the root of your local folder. Syntax is gitignore-style:

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

Rules are evaluated top-to-bottom; negation patterns (`!`) override earlier ignore rules. See gitignore documentation for full pattern syntax.

## Engine

**Engine**: rclone (MIT), embedded in-process via librclone. Single binary — no separate rclone installation needed. All sync logic runs in-memory; no background daemon required (v1 uses periodic bisync via rclone's internal scheduler).

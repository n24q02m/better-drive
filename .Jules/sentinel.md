## 2026-07-22 - Enforce Strict File Permissions in Multi-User Environments
**Vulnerability:** Insecure local file and directory permissions (e.g., `0o644` for log files, `0o755` for sync and work directories, and systemd unit files).
**Learning:** In a multi-user environment, creating files or directories with overly permissive permissions can lead to unauthorized access, allowing other users to read sensitive logs, sync data, or modify configuration files.
**Prevention:** Always enforce strict permissions (`0o600` for files and `0o700` for directories) when creating logs, working directories, configuration files, and systemd autostart units unless broader access is explicitly required and justified.

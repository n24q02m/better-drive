## 2026-07-23 - Enforce Strict File and Directory Permissions
**Vulnerability:** Directories (`MkdirAll`) and files (`WriteFile`, `OpenFile`) were created with overly permissive bits (`0o755` and `0o644`), which could allow local cross-user tampering or info leakage in multi-user environments.
**Learning:** For a local daemon managing sync states and potential user data configurations/logs, even systemd/launchd configs and workdirs must restrict access exclusively to the owner (`0o700` and `0o600`).
**Prevention:** Always verify file/directory creation uses the strictest required permission (`0o600` for files, `0o700` for directories). Do not default to typical shared `0o644`/`0o755` unless access by other users is an explicit, verified requirement.
## 2024-05-24 - Prevent Path Traversal in Go 1.24+

**Vulnerability:** The application was using `os.Open(filepath.Join(localRoot, ".driveignore"))`. This is vulnerable to directory traversal if `.driveignore` is a malicious symbolic link pointing outside of `localRoot` (e.g. to `/etc/shadow`), which could result in an attacker manipulating the application to read arbitrary sensitive files.

**Learning:** `os.OpenRoot` was introduced in Go 1.24 to provide a secure, scoped file system handle. When you need to read files within a specific directory context and want to guarantee that accesses cannot escape that context (e.g., via `..` or symlinks), `os.OpenRoot` is the correct defense-in-depth approach compared to manually resolving and validating paths.

**Prevention:** Use `root, err := os.OpenRoot(baseDir)` followed by `root.Open(file)` instead of `os.Open(filepath.Join(baseDir, file))` for safely handling user-controlled or potentially untrusted file/directory structures.
## 2026-08-08 - Prevent Systemd Command and Specifier Injection
**Vulnerability:** The systemd unit file generated in `autostart_linux.go` used `%s` to inject the executable path directly into the `ExecStart` directive without quoting or escaping. This allows command splitting (if the path contains spaces) and arbitrary systemd specifier/variable injection (using `%` or `$`), which could lead to privilege escalation or unintended command execution.
**Learning:** Systemd unit files require precise escaping rules. Variables (`$`) and specifiers (`%`) must be escaped by doubling them (`$$`, `%%`). Additionally, executable paths must be quoted (e.g. using `%q` in Go) to safely handle spaces in directory names without splitting the command arguments.
**Prevention:** Always escape `%` and `$` in user-controlled inputs or dynamic paths when generating systemd unit files, and ensure the entire executable path is safely quoted.

## 2026-07-23 - Enforce Strict File and Directory Permissions
**Vulnerability:** Directories (`MkdirAll`) and files (`WriteFile`, `OpenFile`) were created with overly permissive bits (`0o755` and `0o644`), which could allow local cross-user tampering or info leakage in multi-user environments.
**Learning:** For a local daemon managing sync states and potential user data configurations/logs, even systemd/launchd configs and workdirs must restrict access exclusively to the owner (`0o700` and `0o600`).
**Prevention:** Always verify file/directory creation uses the strictest required permission (`0o600` for files, `0o700` for directories). Do not default to typical shared `0o644`/`0o755` unless access by other users is an explicit, verified requirement.
## 2024-05-24 - Prevent Path Traversal in Go 1.24+

**Vulnerability:** The application was using `os.Open(filepath.Join(localRoot, ".driveignore"))`. This is vulnerable to directory traversal if `.driveignore` is a malicious symbolic link pointing outside of `localRoot` (e.g. to `/etc/shadow`), which could result in an attacker manipulating the application to read arbitrary sensitive files.

**Learning:** `os.OpenRoot` was introduced in Go 1.24 to provide a secure, scoped file system handle. When you need to read files within a specific directory context and want to guarantee that accesses cannot escape that context (e.g., via `..` or symlinks), `os.OpenRoot` is the correct defense-in-depth approach compared to manually resolving and validating paths.

**Prevention:** Use `root, err := os.OpenRoot(baseDir)` followed by `root.Open(file)` instead of `os.Open(filepath.Join(baseDir, file))` for safely handling user-controlled or potentially untrusted file/directory structures.
## 2024-05-24 - Systemd Unit File Command Injection
**Vulnerability:** A local attacker could create an executable path containing spaces or systemd variables (like `%h` or `$USER`) leading to command injection or unauthorized code execution when the autostart systemd unit file is parsed. The unquoted `%s` in the systemd `ExecStart` line meant paths with spaces were split into multiple arguments, causing failure or execution of unintended binaries.
**Learning:** Generating configuration files that invoke shell or system managers (like systemd) must strictly quote paths and escape interpolation variables specific to that format. It is not enough to just use `filepath` or basic strings.
**Prevention:** Use `%q` in `fmt.Sprintf` for systemd `ExecStart` directives to securely quote strings in Go. Always escape `%` (with `%%`) and `$` (with `$$`) if the string path is not under application control to prevent systemd's built-in variable expansion from executing or injecting untrusted content.

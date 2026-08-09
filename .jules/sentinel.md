## 2026-07-23 - Enforce Strict File and Directory Permissions
**Vulnerability:** Directories (`MkdirAll`) and files (`WriteFile`, `OpenFile`) were created with overly permissive bits (`0o755` and `0o644`), which could allow local cross-user tampering or info leakage in multi-user environments.
**Learning:** For a local daemon managing sync states and potential user data configurations/logs, even systemd/launchd configs and workdirs must restrict access exclusively to the owner (`0o700` and `0o600`).
**Prevention:** Always verify file/directory creation uses the strictest required permission (`0o600` for files, `0o700` for directories). Do not default to typical shared `0o644`/`0o755` unless access by other users is an explicit, verified requirement.
## 2024-05-24 - Prevent Path Traversal in Go 1.24+

**Vulnerability:** The application was using `os.Open(filepath.Join(localRoot, ".driveignore"))`. This is vulnerable to directory traversal if `.driveignore` is a malicious symbolic link pointing outside of `localRoot` (e.g. to `/etc/shadow`), which could result in an attacker manipulating the application to read arbitrary sensitive files.

**Learning:** `os.OpenRoot` was introduced in Go 1.24 to provide a secure, scoped file system handle. When you need to read files within a specific directory context and want to guarantee that accesses cannot escape that context (e.g., via `..` or symlinks), `os.OpenRoot` is the correct defense-in-depth approach compared to manually resolving and validating paths.

**Prevention:** Use `root, err := os.OpenRoot(baseDir)` followed by `root.Open(file)` instead of `os.Open(filepath.Join(baseDir, file))` for safely handling user-controlled or potentially untrusted file/directory structures.
## 2026-08-09 - Prevent Systemd Unit File Injection
**Vulnerability:** When generating `~/.config/systemd/user/better-drive.service`, the executable path was formatted into the template using `%s` without escaping systemd specifiers (`%`) or variables (`$`), and without quoting. This could allow command splitting or injection if the path contains malicious characters.
**Learning:** Systemd unit files have their own syntax rules. Any user-controlled or dynamically generated string inserted into a unit file must escape systemd's special characters (`%` -> `%%`, `$` -> `$$`) and be properly quoted, just like preventing shell injection.
**Prevention:** Always use `strings.ReplaceAll` to escape `%` and `$` before formatting strings into systemd unit templates, and use Go's `%q` format verb (or literal double quotes) to ensure the path is safely enclosed.

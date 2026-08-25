## 2026-07-23 - Enforce Strict File and Directory Permissions
**Vulnerability:** Directories (`MkdirAll`) and files (`WriteFile`, `OpenFile`) were created with overly permissive bits (`0o755` and `0o644`), which could allow local cross-user tampering or info leakage in multi-user environments.
**Learning:** For a local daemon managing sync states and potential user data configurations/logs, even systemd/launchd configs and workdirs must restrict access exclusively to the owner (`0o700` and `0o600`).
**Prevention:** Always verify file/directory creation uses the strictest required permission (`0o600` for files, `0o700` for directories). Do not default to typical shared `0o644`/`0o755` unless access by other users is an explicit, verified requirement.
## 2024-05-24 - Prevent Path Traversal in Go 1.24+

**Vulnerability:** The application was using `os.Open(filepath.Join(localRoot, ".driveignore"))`. This is vulnerable to directory traversal if `.driveignore` is a malicious symbolic link pointing outside of `localRoot` (e.g. to `/etc/shadow`), which could result in an attacker manipulating the application to read arbitrary sensitive files.

**Learning:** `os.OpenRoot` was introduced in Go 1.24 to provide a secure, scoped file system handle. When you need to read files within a specific directory context and want to guarantee that accesses cannot escape that context (e.g., via `..` or symlinks), `os.OpenRoot` is the correct defense-in-depth approach compared to manually resolving and validating paths.

**Prevention:** Use `root, err := os.OpenRoot(baseDir)` followed by `root.Open(file)` instead of `os.Open(filepath.Join(baseDir, file))` for safely handling user-controlled or potentially untrusted file/directory structures.
## 2026-07-29 - Prevent Command Injection via Subprocess with Variables
**Vulnerability:** External input or variables passed to `exec.Command` can be risky if unvalidated (CWE-78 / Gosec G204). While Go natively resists shell injection when arguments are split, passing unsanitized file paths to GUI commands like `explorer` is a path traversal and logic risk.
**Learning:** Always sanitize inputs meant for sub-processes, even in thick clients or GUI trays. `filepath.Clean` provides essential defense-in-depth against crafted paths traversing out of bounds.
**Prevention:** Always wrap external inputs via `filepath.Clean()` before execution. For known-safe binaries, document the intentional use via a `/* #nosec G204 */` linter suppression comment to differentiate them from actual unvalidated risk points.
## 2026-08-07 - Secure Path Resolution with os.OpenRoot
**Vulnerability:** Path traversal (CWE-22) flagged by gosec due to taint analysis on os.Getenv("APPDATA") when checking file existence using os.Stat(filepath.Join(...)).
**Learning:** Combining tainted environment variables with filepath.Join and os.Stat is vulnerable to path traversal. filepath.Clean() is merely lexical and doesn't enforce strict boundaries.
**Prevention:** Use Go 1.24+ os.OpenRoot() to safely scope filesystem access to the expected root directory, and call root.Stat() on the resulting handle to prevent escapes.

## 2026-08-10 - Prevent Systemd Command Injection
**Vulnerability:** Unescaped executable paths in systemd unit templates allowed command splitting via spaces and variable/specifier injection via `%` and `$`.
**Learning:** Systemd expands variables and specifiers (like `%h` or `$USER`) in `ExecStart`. If a path contains these characters, it can alter the execution context or run arbitrary commands. Additionally, unquoted paths with spaces cause systemd to split arguments incorrectly.
**Prevention:** Always escape `%` to `%%` and `$` to `$$` in user-controlled or variable paths inserted into systemd units, and use double quotes (or `%q` in Go) to prevent command splitting.
## 2026-08-11 - Explicitly handle or document unhandled errors
**Vulnerability:** Unhandled errors, such as ignoring the return value of resource cleanups or core application logic, can silently obscure resource exhaustion or logic bypasses (CWE-703), which gosec flags as G104.
**Learning:** In Go, blindly suppressing unhandled errors (e.g., using `_ = err`) without comment makes the code unauditable. For cases where an error is truly unactionable (like a best-effort `os.Remove` on a temp file or a read-only `root.Close()`), it must be explicitly documented.
**Prevention:** Document intentional error suppressions with `// #nosec G104 -- [reason]` to prove to auditors and linters that the ignored error was consciously evaluated as benign and not a forgotten security or reliability boundary.
## 2026-08-25 - Prevent Path Traversal in Cleanup Journals
**Vulnerability:** `internal/cleanup/journal.go` used `os.Open` and `os.OpenFile` which could be vulnerable to path traversal via symlinks or crafted path strings.
**Learning:** `os.OpenRoot` scopes the path correctly to its expected base directory.
**Prevention:** Use Go 1.24+ `os.OpenRoot` when dealing with potentially user-controlled file paths, even for journals or local configuration paths.

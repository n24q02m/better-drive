## 2025-02-23 - Restrict permissions on sensitive directories
**Vulnerability:** Directories created by `os.MkdirAll(..., 0o755)` in `internal/engine/engine.go` (specifically `p.Workdir` for app state and `p.Path1` for local sync directory) had overly permissive access rights. This allowed read access to other local users, which can expose sensitive synchronized data.
**Learning:** Hardcoding `0o755` for sensitive file or directory creation exposes data to all users on the same machine. This is a common security pitfall.
**Prevention:** Always follow the principle of least privilege. For sensitive operations like creating synchronization or work directories, use `0o700` so only the owner has access.

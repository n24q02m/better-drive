## 2026-07-19 - Restrictive File Permissions
**Vulnerability:** Weak permissions (`0o644` and `0o755`) on log files and sync directories could allow other users on the system to read sensitive data or metadata.
**Learning:** Default permissions must always adhere to the principle of least privilege, especially for files containing user sync state.
**Prevention:** Always use `0o600` for files and `0o700` for directories that handle user data unless broader access is explicitly required.

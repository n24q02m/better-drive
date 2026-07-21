
## 2024-05-24 - [MEDIUM] Fix insecure file and directory permissions
**Vulnerability:** Directories and files created by better-drive were using weak permissions (`0o755` for directories, `0o644` for files). In a multi-user system, this allows other users to potentially access sensitive sync configurations, sync states (like baseline files in `workdir`), log files, or autostart definitions.
**Learning:** For an application that handles personal/cloud files and credentials (even indirectly through config locations), local multi-user isolation is critical. Relying on default wide-open permissions like `0o755`/`0o644` breaks the principle of least privilege.
**Prevention:** Always scope file and directory permissions defensively. Use `0o700` for application state/sync directories (`os.MkdirAll`) and `0o600` for application-written files (like logs or systemd units via `os.WriteFile`/`os.OpenFile`).

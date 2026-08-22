1. **Analyze Security Issue**: In `internal/tray/tray_systray.go`, the function `openFolder` uses `exec.Command("explorer", cleanPath).Start()`. According to the memory guidelines: *"When passing paths to external utilities like Windows `explorer` via `exec.Command`, `filepath.Abs` or `filepath.Clean` alone provide no real security benefit against malicious file execution. Always validate the resolved absolute path against a trusted root directory (e.g., using `strings.HasPrefix`) or use `os.Stat` to confirm the target is strictly a safe directory before execution."* If `path` happens to point to an executable instead of a folder, `explorer` might just launch it.
2. **Implement Fix in `openFolder`**:
   - Add the `"os"` package to the imports in `internal/tray/tray_systray.go`.
   - Before executing the command in `openFolder`, check `os.Stat` to ensure the target is actually a directory.
3. **Run tests & verification**: Execute `go fmt`, `gosec`, and `go test` to confirm everything is functionally correct.
4. **Complete pre-commit steps to ensure proper testing, verification, review, and reflection are done.**: Invoke the necessary API.
5. **Add Sentinel Journal Entry**: Document this learning.
6. **Create PR**: Create PR with the specific Sentinel format.

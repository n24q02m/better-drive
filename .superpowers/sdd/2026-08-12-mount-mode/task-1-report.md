# Task 1 report - engine streaming mount

## Verdict

Task 1 engine slice đã hoàn tất trong đúng ownership code:

- `internal/engine/engine.go`
- `internal/engine/runner.go`
- `internal/engine/engine_test.go`

Không sửa CLI, docs, config hay plan. File report này được thêm theo yêu cầu giao việc.

## Root cause / design

Root cause của khoảng trống mount mode là engine hiện tại chỉ có seam `runner`
cho lệnh ngắn hạn kiểu request/response: nó capture stdout/stderr sau khi child
process kết thúc và không nhận `context.Context`. Cách đó phù hợp với
`copy/sync/bisync`, nhưng không đúng contract của `rclone mount`, vốn là một
foreground process sống lâu, phải:

- stream stdout/stderr thật ra terminal ngay khi process đang chạy;
- bị hủy bởi context để Ctrl+C/termination kết thúc child process;
- vẫn giữ stderr trong error để lớp CLI sau này map remediation;
- không giữ `syncMu`, vì mount dài hạn không được chặn các sync cycle độc lập.

Thiết kế thực thi tối thiểu cho Task 1:

1. Giữ nguyên `runner` hiện có cho lệnh capture ngắn hạn.
2. Thêm `streamRunner` riêng cho process dài hạn, được `New()` wire vào
   `exec.CommandContext` + `hideConsole`.
3. Thêm `MountParams` và `Engine.Mount(ctx, params)` để dựng argv:

   `rclone [--config <path>] mount <remote:path> <mountpoint> --vfs-cache-mode full [--read-only]`

4. Dùng `io.MultiWriter` để vừa stream stderr thật, vừa capture lại stderr vào
   error khi mount fail.

## Files changed

- `internal/engine/runner.go`
  - thêm `streamRunner`
  - thêm `execStreamRunner(bin)` dùng `exec.CommandContext`
- `internal/engine/engine.go`
  - thêm field `stream`
  - `New()` wire cả captured runner và streaming runner
  - thêm `streamExec(...)`
  - thêm `MountParams`
  - thêm `Engine.Mount(ctx, params)`
- `internal/engine/engine_test.go`
  - thêm helper fake streaming engine
  - thêm test mount cho argv/config/cache/read-only
  - thêm test stdout/stderr + stderr-in-error
  - thêm test context propagation
  - thêm regression guard `Mount` không giữ `syncMu`

## Red test first

Tôi thêm test đỏ trước rồi chạy để quan sát fail do feature còn thiếu.

Command:

```powershell
go test ./internal/engine -run 'TestMount|TestNewResolvesRunner' -count=1
```

Observed output:

```text
# github.com/n24q02m/better-drive/internal/engine [github.com/n24q02m/better-drive/internal/engine.test]
internal\engine\engine_test.go:26:70: undefined: streamRunner
internal\engine\engine_test.go:33:39: unknown field stream in struct literal of type Engine
internal\engine\engine_test.go:50:7: e.stream undefined (type *Engine has no field or method stream)
internal\engine\engine_test.go:63:12: e.Mount undefined (type *Engine has no field or method Mount)
internal\engine\engine_test.go:63:40: undefined: MountParams
internal\engine\engine_test.go:90:12: e.Mount undefined (type *Engine has no field or method Mount)
internal\engine\engine_test.go:90:40: undefined: MountParams
internal\engine\engine_test.go:125:11: e.Mount undefined (type *Engine has no field or method Mount)
internal\engine\engine_test.go:125:39: undefined: MountParams
internal\engine\engine_test.go:161:13: e.Mount undefined (type *Engine has no field or method Mount)
internal\engine\engine_test.go:161:13: too many errors
FAIL	github.com/n24q02m/better-drive/internal/engine [build failed]
FAIL
```

Đây là fail đúng lý do mong muốn: primitive cho streaming mount chưa tồn tại.

## Implementation

### 1. Streaming runner injectable

Tách seam mới:

- `type streamRunner func(ctx context.Context, stdout, stderr io.Writer, args ...string) error`
- `execStreamRunner(bin)` chạy `exec.CommandContext(ctx, bin, args...)`
- giữ `hideConsole(cmd)` để không làm thay đổi behavior Windows hiện có

### 2. Engine mount slice

`Engine.Mount`:

- prepend `--config` thông qua `streamExec` giống các lệnh khác;
- luôn append `--vfs-cache-mode full`;
- append `--read-only` khi được yêu cầu;
- default `nil` stdout/stderr sang `io.Discard`;
- tee stderr bằng `io.MultiWriter(stderrOut, &stderrBuf)`;
- wrap lỗi dạng `rclone mount: <exit error>: <trimmed stderr>`.

### 3. Concurrency boundary

`Mount` không chạm `syncMu`, nên một mount đang sống không serialize `Copy`,
`Sync`, `Bisync`.

## Commands and observed output

### Format

```powershell
gofmt -w internal/engine/engine.go internal/engine/runner.go internal/engine/engine_test.go
```

Observed output: none, exit 0.

### Focused mount tests after implementation

```powershell
go test ./internal/engine -run TestMountBuildsStreamingArgs -count=1 -v -timeout 10s
```

```text
=== RUN   TestMountBuildsStreamingArgs
=== RUN   TestMountBuildsStreamingArgs/default_cache_mode_and_config_prefix
=== RUN   TestMountBuildsStreamingArgs/read_only_appends_flag
--- PASS: TestMountBuildsStreamingArgs (0.00s)
    --- PASS: TestMountBuildsStreamingArgs/default_cache_mode_and_config_prefix (0.00s)
    --- PASS: TestMountBuildsStreamingArgs/read_only_appends_flag (0.00s)
PASS
ok  	github.com/n24q02m/better-drive/internal/engine	2.076s
```

```powershell
go test ./internal/engine -run TestMountStreamsOutputAndRetainsStderrInError -count=1 -v -timeout 10s
```

```text
=== RUN   TestMountStreamsOutputAndRetainsStderrInError
--- PASS: TestMountStreamsOutputAndRetainsStderrInError (0.00s)
PASS
ok  	github.com/n24q02m/better-drive/internal/engine	1.738s
```

```powershell
go test ./internal/engine -run TestMountPropagatesContextToStreamingRunner -count=1 -v -timeout 10s
```

```text
=== RUN   TestMountPropagatesContextToStreamingRunner
--- PASS: TestMountPropagatesContextToStreamingRunner (0.00s)
PASS
ok  	github.com/n24q02m/better-drive/internal/engine	2.381s
```

```powershell
go test ./internal/engine -run TestMountDoesNotSerializeWithSyncOps -count=1 -v -timeout 10s
```

```text
=== RUN   TestMountDoesNotSerializeWithSyncOps
--- PASS: TestMountDoesNotSerializeWithSyncOps (0.00s)
PASS
ok  	github.com/n24q02m/better-drive/internal/engine	1.198s
```

### Repository validation

```powershell
go build ./...
```

Observed output: none, exit 0.

```powershell
go test ./internal/engine -count=1
```

```text
ok  	github.com/n24q02m/better-drive/internal/engine	1.816s
```

Exact rerun:

```powershell
go test ./internal/engine -count=1
```

```text
ok  	github.com/n24q02m/better-drive/internal/engine	3.971s
```

```powershell
go test -race ./internal/engine -run 'TestMount|TestSyncOpsSerialize' -count=1
```

```text
ok  	github.com/n24q02m/better-drive/internal/engine	2.722s
```

```powershell
go test ./...
```

```text
?   	github.com/n24q02m/better-drive	[no test files]
ok  	github.com/n24q02m/better-drive/internal/autostart	(cached)
ok  	github.com/n24q02m/better-drive/internal/cli	3.388s
ok  	github.com/n24q02m/better-drive/internal/config	(cached)
ok  	github.com/n24q02m/better-drive/internal/engine	2.442s
ok  	github.com/n24q02m/better-drive/internal/exitcode	(cached)
ok  	github.com/n24q02m/better-drive/internal/output	(cached)
ok  	github.com/n24q02m/better-drive/internal/paths	(cached)
ok  	github.com/n24q02m/better-drive/internal/syncloop	1.455s
ok  	github.com/n24q02m/better-drive/internal/tray	1.146s
?   	github.com/n24q02m/better-drive/internal/version	[no test files]
```

```powershell
go vet ./...
```

Observed output: none, exit 0.

## Self-review

- Scope giữ đúng engine slice; không động CLI/docs/config/plan.
- `Mount` tái dùng chuẩn prefix `--config` hiện có, nên mount độc lập với pair
  config như thiết kế yêu cầu.
- stderr vẫn được stream thật và vẫn nằm trong error để layer sau map
  remediation theo OS.
- `Mount` không lock `syncMu`; đã có regression test xác nhận sync path không
  bị block bởi mount đang chạy.
- Không thêm remote-path validation hay remediation text ở engine layer trong
  Task 1 vì brief hiện tại chỉ yêu cầu engine slice; các phần đó để CLI/E2E
  slice sau xử lý.

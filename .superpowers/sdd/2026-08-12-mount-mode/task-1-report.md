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

## Fix round 1/5 — review corrections (2026-08-13)

### Root cause và design correction

1. `exec.CommandContext` đã dừng process khi context bị hủy, nhưng
   `cmd.Run()` vẫn trả lỗi process (`*exec.ExitError`) trên đường kill. Vì
   `execStreamRunner` trả nguyên lỗi đó, caller không thể dùng
   `errors.Is(err, context.Canceled)` hoặc
   `errors.Is(err, context.DeadlineExceeded)` theo public contract. Runner nay
   ưu tiên `ctx.Err()` khi `Run` thất bại và context đã kết thúc; lỗi process
   không liên quan context vẫn được giữ nguyên.
2. `io.MultiWriter(stderrOut, &stderr)` có hai vấn đề với mount dài hạn:
   `bytes.Buffer` tăng không giới hạn, và lỗi từ terminal writer đầu tiên làm
   `MultiWriter` dừng trước khi buffer nhận evidence. Thiết kế mới dùng một
   writer duy nhất: giữ tail trước, với giới hạn cứng 64 KiB, rồi forward cùng
   bytes đó tới terminal để giữ live stderr. Vì capture xảy ra trước forward,
   evidence vẫn còn khi terminal writer trả lỗi.
3. Test streaming cũ chỉ đọc buffer sau khi `Mount` đã trả về; do đó chưa chứng
   minh output được stream trong lúc process còn chạy. Test mutex cũ dùng mốc
   200 ms nên phụ thuộc scheduler. Cả hai nay dùng channel handshake xác định;
   timeout 5 giây chỉ là deadlock guard.
4. Không thêm remediation theo Windows/Linux/macOS vào engine. Engine chỉ giữ
   đủ stderr tail cho remediation driver thuộc Task 2 CLI contract.

### Files

- `internal/engine/runner.go`: normalize lỗi hủy context của streaming process.
- `internal/engine/engine.go`: bounded stderr tail writer, capture-before-live-forward.
- `internal/engine/engine_test.go`: helper-process cancellation test, live-stream
  handshakes, bounded-tail/writer-failure tests và deterministic mutex handshake.
- `.superpowers/sdd/2026-08-12-mount-mode/task-1-report.md`: nối evidence của fix round này.

### TDD RED evidence

Cancellation contract được chạy đỏ bằng process thật là chính test binary:

```powershell
& 'C:\Users\n24q02m-wpc\go\pkg\mod\golang.org\toolchain@v0.0.1-go1.26.5.windows-amd64\bin\go.exe' test ./internal/engine -run '^TestExecStreamRunnerNormalizesContextCancellation$' -count=1 -v -timeout 20s -vet=off
```

Observed: exit 1; cả hai case đỏ vì runner trả `exit status 1` thay vì lỗi
context:

```text
=== RUN   TestExecStreamRunnerNormalizesContextCancellation/context_canceled
    engine_test.go:128: execStreamRunner() error = exit status 1, want errors.Is(..., context canceled)
=== RUN   TestExecStreamRunnerNormalizesContextCancellation/context_deadline_exceeded
    engine_test.go:128: execStreamRunner() error = exit status 1, want errors.Is(..., context deadline exceeded)
--- FAIL: TestExecStreamRunnerNormalizesContextCancellation (2.00s)
FAIL
FAIL github.com/n24q02m/better-drive/internal/engine 3.809s
```

Bounded capture và terminal-writer independence được chạy đỏ trước production
change:

```powershell
& 'C:\Users\n24q02m-wpc\go\pkg\mod\golang.org\toolchain@v0.0.1-go1.26.5.windows-amd64\bin\go.exe' test ./internal/engine -run '^(TestMountStreamsOutputAndRetainsStderrInError|TestMountRetainsBoundedStderrTail|TestMountRetainsStderrWhenLiveWriterFails|TestMountDoesNotSerializeWithSyncOps)$' -count=1 -v -timeout 30s -vet=off
```

Observed: exit 1. Hai handshake tests đã pass; hai regression mới fail đúng root
cause:

```text
--- PASS: TestMountStreamsOutputAndRetainsStderrInError
=== RUN   TestMountRetainsBoundedStderrTail
    engine_test.go:307: retained stderr length = 65580, want at most 65536
--- FAIL: TestMountRetainsBoundedStderrTail
=== RUN   TestMountRetainsStderrWhenLiveWriterFails
    engine_test.go:340: Mount() error = rclone mount: terminal writer failed, want retained stderr evidence "WinFsp driver missing"
--- FAIL: TestMountRetainsStderrWhenLiveWriterFails
--- PASS: TestMountDoesNotSerializeWithSyncOps
FAIL
FAIL github.com/n24q02m/better-drive/internal/engine 3.896s
```

`-vet=off` chỉ dùng cho focused RED để tách assertion khỏi thời gian vet/build;
full `go vet ./...` vẫn được chạy và pass ở verification cuối.

### Implementation

- `execStreamRunner` gọi `cmd.Run()`, rồi trả `ctx.Err()` khi process thất bại
  sau cancellation/deadline; các lỗi process thông thường vẫn trả nguyên trạng.
- `mountStderrWriter` giữ đúng tối đa 64 KiB cuối, bao gồm trường hợp một write
  lớn hơn limit và trường hợp tail cắt qua nhiều write; mọi byte vẫn được
  forward live tới caller.
- Capture xảy ra trước live write nên error của terminal writer vẫn được wrap
  đồng thời stderr evidence vẫn xuất hiện trong error của `Mount`.
- Helper process báo `helper ready` trước khi test trigger context, tránh race
  với startup. Streaming stdout/stderr đều có handshake trước khi fake runner
  được release. Mutex test chờ runner của `Copy` thực sự enter thay vì suy luận
  từ một scheduling window.
- `Mount` tiếp tục không giữ `syncMu`, giữ `--config <path>` prefix, và không
  chứa remediation text theo OS.

### Commands và observed output

Formatting:

```powershell
gofmt -w internal/engine/engine.go internal/engine/runner.go internal/engine/engine_test.go
```

Observed: exit 0, không có output.

Focused GREEN:

```powershell
& 'C:\Users\n24q02m-wpc\go\pkg\mod\golang.org\toolchain@v0.0.1-go1.26.5.windows-amd64\bin\go.exe' test ./internal/engine -run '^(TestExecStreamRunnerNormalizesContextCancellation|TestMount)' -count=1 -v -timeout 30s -vet=off
```

Observed: exit 0; hai helper-process cancellation cases và toàn bộ Mount tests
pass, package `ok` trong 2.565s.

Exact engine run và exact rerun:

```powershell
go test ./internal/engine -count=1
go test ./internal/engine -count=1
```

Observed: cả hai exit 0; lần lượt:

```text
ok github.com/n24q02m/better-drive/internal/engine 2.739s
ok github.com/n24q02m/better-drive/internal/engine 14.658s
```

Race-focused engine verification:

```powershell
go test -race ./internal/engine -run '^(TestExecStreamRunnerNormalizesContextCancellation|TestMount.*|TestSyncOpsSerialize)$' -count=1 -timeout 60s
```

Observed: exit 0:

```text
ok github.com/n24q02m/better-drive/internal/engine 3.483s
```

Full test suite:

```powershell
go test ./... -count=1
```

Observed: exit 0; mọi package pass, gồm `internal/config` 116.469s và
`internal/engine` 7.267s; root và `internal/version` báo `[no test files]`.

Static analysis và build:

```powershell
go vet ./...
go build ./...
```

Observed: cả hai exit 0, không có output.

### Self-review

- Diff production bám đúng hai review root cause: context cancellation và
  bounded/capture-first stderr; không có refactor ngoài scope.
- `rg` đối chứng trả explicit `external references to changed mount symbols: 0`;
  blast radius nằm trong engine API/tests đã kiểm tra.
- Live stream tests chỉ đọc writer sau channel happens-before và trong khi
  runner còn block; race test pass, không phát hiện data race.
- Error vẫn wrap root error bằng `%w`, đồng thời giữ stderr tail phục vụ Task 2.
- Không sửa CLI/docs/config/plan, không thêm OS remediation, không push.
- Không còn finding actionable trong ownership của fix round 1/5.

# Contributing to better-drive

## Prerequisites

- [Go](https://go.dev/dl/) matching the version in `go.mod` (currently 1.26.x)
- [`rclone`](https://rclone.org) on `PATH` (the sync engine better-drive shells out to; `internal/engine`'s tests fake it out, so it is not required to run the unit suite, only to run the binary itself)

## First-time setup

```bash
git clone https://github.com/n24q02m/better-drive
cd better-drive
go build ./...
```

The repository includes optional `mise` tasks and pre-commit hooks. If those
tools are installed, initialize them with:

```bash
mise install
pre-commit install
pre-commit install --hook-type commit-msg
```

The equivalent build and test shortcuts are `mise run build` and
`mise run test`.

## Running tests

```bash
go test ./...
```

For the same coverage numbers CI enforces:

```bash
go test ./... -race -coverprofile=coverage.out -covermode=atomic
go tool cover -func=coverage.out | tail -1
```

## Running the linter

```bash
python scripts/check_gofmt.py
go vet ./...
```

To format a changed Go file, run `gofmt -w <changed-go-files>` explicitly.

## Commit convention

Only two prefixes are used in this repo:

- `feat:` — new features
- `fix:` — bug fixes

No other type (`chore:`, `docs:`, `refactor:`, `ci:`, ...) and no `!` breaking-change marker.

## Running the binary against a scratch config

`better-drive` reads its config from the OS config dir by default, but
`BETTER_DRIVE_CONFIG` overrides the path — point it at a throwaway file so you
never touch a real config while developing:

```bash
cat > /tmp/bd-dev-config.toml <<'EOF'
[[pair]]
local = "/tmp/bd-dev-src"
remote = "gdrive:bd-dev-dst"
interval = "30s"
mode = "copy"
EOF
mkdir -p /tmp/bd-dev-src

BETTER_DRIVE_CONFIG=/tmp/bd-dev-config.toml go run . status
BETTER_DRIVE_CONFIG=/tmp/bd-dev-config.toml go run . sync --dry-run
```

`gdrive` above must already be a configured rclone remote (`rclone config` or
`better-drive setup`) for `sync`/`run` to succeed; `status` never touches the
network.

## Pull requests

- One PR per feature/fix.
- Tests required; CI enforces a coverage floor (see `.github/workflows/ci.yml`).
- All CI checks must pass.

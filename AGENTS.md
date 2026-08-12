# Agent instructions

## Repository scope

better-drive is a cross-platform Go application that shells out to `rclone` for Google Drive transfers. Keep changes focused on the requested behavior, preserve the existing command-line and configuration interfaces, and avoid unrelated refactors.

## Validation

Run the checks that match the change:

```bash
gofmt -w <changed-go-files>
go build ./...
go test ./...
go vet ./...
```

The CI workflow also runs race-enabled tests and enforces the repository's coverage floor.

## Change and commit policy

- Add or update tests for behavior changes.
- Do not commit credentials, tokens, personal data, or machine-specific paths.
- Use `feat:` or `fix:` commit subjects only; scoped forms such as `fix(deps):` are valid.
- Keep release and repository-settings changes explicit and separately reviewable.

## Release safety

Do not create or push release tags from a development change. Release workflows are started deliberately through the repository's documented release process.

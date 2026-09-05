1. **Optimize `gitProcessEnvironment` in `internal/cleanup/gitstore.go`**
   - The current code uses `strings.HasPrefix(strings.ToUpper(name), "GIT_")` to filter environment variables. This allocates a new uppercase string for every environment variable, causing unnecessary heap allocations and garbage collection pressure in a function that may run frequently when interacting with git.
   - We will replace `strings.ToUpper(name)` with a zero-allocation, case-insensitive prefix check logic. Go's `strings` does not have a direct `HasPrefixFold` but we can write a simple and fast custom check or use `len(name) >= 4 && strings.EqualFold(name[:4], "GIT_")` to eliminate the allocation and drastically improve performance (as verified by benchmarking).

2. **Run tests**
   - Execute `go test ./...` and `gofmt -s -w .` to verify that the change does not break any functionality and is properly formatted.

3. **Complete pre-commit steps**
   - Complete pre-commit steps to ensure proper testing, verification, review, and reflection are done.

4. **Submit PR**
   - Commit the change and submit with a message and description tailored to Bolt's rules.

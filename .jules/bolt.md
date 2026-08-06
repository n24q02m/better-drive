## 2024-05-18 - Avoid strings.HasPrefix for single-character checks
**Learning:** Using `strings.HasPrefix(line, "#")` allocates in some contexts or involves extra function call overhead compared to direct byte indexing `line[0] == '#'`.
**Action:** When optimizing Go performance, use zero-allocation byte indexing instead of `strings.HasPrefix` for single-character checks, making sure to verify the string is not empty (`line != ""`) beforehand to prevent out-of-bounds panics.

## 2024-05-18 - Pre-allocate slice capacities
**Learning:** Returning `var out []string` and appending to it without capacity allocation causes hidden memory re-allocations during `append()`.
**Action:** Always pre-allocate slice capacities (e.g., `make([]string, 0, len(input))`) to prevent hidden re-allocations. Explicitly handle nil/empty inputs first (e.g., `if len(input) == 0 { return nil }`) if the function contract expects returning `nil`.

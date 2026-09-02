## 2024-05-24 - Pre-allocating slices and zero-allocation string parsing in Go
**Learning:** When generating configuration strings or parsing rules, avoid `strings.HasPrefix` for single-character checks because it invokes a function call and can have overhead. Additionally, hidden allocations inside loops like `append()` can cause performance bottlenecks.
**Action:** Use zero-allocation byte indexing (e.g., `string[0] == '#'`) instead of `strings.HasPrefix` for single-character checks, always ensuring you check for an empty string first (`string != ""`). Pre-allocate slice capacities (e.g., `make([]T, 0, len(input))`) to prevent hidden re-allocations during `append()`. Always ensure you explicitly handle nil/empty inputs first (e.g., `if len(input) == 0 { return nil }`).

## 2025-02-26 - Pre-allocate strings.Builder for Slice Joining with Trailing Separators
**Learning:** Using `strings.Join(slice, sep) + sep` results in a hidden double allocation: `Join` allocates its own string, and the subsequent `+` operator allocates a new string to append the final separator. This is particularly wasteful when generating large configurations or filter lists.
**Action:** Replace `strings.Join(slice, sep) + sep` with a `strings.Builder`. Calculate the exact needed capacity first (`len(slice) * len(sep)` + sum of all string lengths), call `Grow(capacity)`, and iterate over the slice calling `WriteString` and `WriteByte`/`WriteString` for the separator.

## 2024-08-11 - Pre-allocating filter arrays
**Learning:** `TranslateIgnoreLines` was appending to a dynamic array without pre-allocation, leading to allocations that could be prevented, and using `strings.HasPrefix` for single-byte checks when standard array indexing is zero-allocation.
**Action:** Always check `len(input) == 0` for an early return, use `make([]T, 0, len(input))` to pre-allocate exact slice capacities, and use single byte index checks like `str[0] == '#'` over `strings.HasPrefix` for single character lookups.

## 2026-08-29 - Avoid immaterial micro-optimizations on paths
**Learning:** The PR optimizing `strings.Split` with iterative `strings.Cut` loops for path processing was rejected as "bot-generated noise" / "immaterial micro-optimization".
**Action:** Avoid micro-optimizing path parsing logic on cold or moderately hot paths if it requires complex looping constructs, as it may be deemed unnecessary noise by maintainers. Only apply such optimizations if there is a proven, measurable bottleneck.

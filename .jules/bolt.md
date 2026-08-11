## 2025-02-26 - Pre-allocate strings.Builder for Slice Joining with Trailing Separators
**Learning:** Using `strings.Join(slice, sep) + sep` results in a hidden double allocation: `Join` allocates its own string, and the subsequent `+` operator allocates a new string to append the final separator. This is particularly wasteful when generating large configurations or filter lists.
**Action:** Replace `strings.Join(slice, sep) + sep` with a `strings.Builder`. Calculate the exact needed capacity first (`len(slice) * len(sep)` + sum of all string lengths), call `Grow(capacity)`, and iterate over the slice calling `WriteString` and `WriteByte`/`WriteString` for the separator.

## 2024-08-11 - Pre-allocating filter arrays
**Learning:** `TranslateIgnoreLines` was appending to a dynamic array without pre-allocation, leading to allocations that could be prevented, and using `strings.HasPrefix` for single-byte checks when standard array indexing is zero-allocation.
**Action:** Always check `len(input) == 0` for an early return, use `make([]T, 0, len(input))` to pre-allocate exact slice capacities, and use single byte index checks like `str[0] == '#'` over `strings.HasPrefix` for single character lookups.

## 2024-08-08 - Slice allocation and prefix checking
**Learning:** In Go, pre-allocating slices using `make([]T, 0, capacity)` and using direct byte indexing (e.g., `str[0] == '#'`) instead of `strings.HasPrefix` (after checking for emptiness) can noticeably reduce hidden re-allocations and CPU overhead in frequent string processing loops.
**Action:** Always consider pre-allocating slice capacities when the target size is known or bounded, and use byte indexing for simple ASCII character checks.

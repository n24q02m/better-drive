## 2024-08-09 - Pre-allocating slice capacity and byte-indexing
**Learning:** When parsing configuration lists, unallocated slices can cause hidden re-allocations during `append()`. Using `strings.HasPrefix` for single-character checks introduces unnecessary overhead.
**Action:** Always pre-allocate slice capacities `make([]T, 0, len(input))` when translating lines. Use zero-allocation byte indexing `line[0] == '#'` instead of `strings.HasPrefix` for single-character checks, ensuring `line != ""` is checked first.

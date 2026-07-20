## 2025-01-24 - Zero-allocation string splitting for large outputs
**Learning:** Parsing large string outputs (like those from external `rclone` processes) using `strings.Split` causes significant memory allocations and GC overhead because it creates a slice containing every line.
**Action:** Use an iterative `strings.Cut(s, "\n")` loop instead. This drops allocations to zero for simple iteration, or drastically reduces them when accumulating a slice (by combining it with `strings.Count(s, "\n")` to pre-allocate exact capacity).

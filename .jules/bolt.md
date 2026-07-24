## 2025-01-24 - Zero-allocation string splitting for large outputs
**Learning:** Parsing large string outputs (like those from external `rclone` processes) using `strings.Split` causes significant memory allocations and GC overhead because it creates a slice containing every line.
**Action:** Use an iterative `strings.Cut(s, "\n")` loop instead. This drops allocations to zero for simple iteration, or drastically reduces them when accumulating a slice (by combining it with `strings.Count(s, "\n")` to pre-allocate exact capacity).

## 2025-01-24 - Rejected Micro-optimization (strings.Cut vs strings.Split)
**Learning:** Replacing `strings.Split` with iterative `strings.Cut` in non-hot-path code (e.g. infrequent parsing of rclone output or .driveignore) is considered churn. It sacrifices readability for a negligible, unmeasured benefit.
**Action:** Do not apply micro-optimizations in cold paths. Stick to idiomatic forms like `strings.Split` unless a measurable or hot-path benefit is demonstrated.

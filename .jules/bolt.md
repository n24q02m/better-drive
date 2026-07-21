## 2024-07-21 - Replace strings.Split with iterative strings.Cut
**Learning:** Using `strings.Split` on large `rclone` string outputs incurs unnecessary memory allocation and garbage collection overhead.
**Action:** Prefer using `strings.Cut` iteratively in a loop and pre-allocate slices using `strings.Count(s, "\n")` when accumulating results from large string parsing.

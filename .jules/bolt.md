## 2023-10-24 - Avoid strings.Split for Large Process Outputs
**Learning:** Parsing large shell process outputs (like `rclone lsf` or `listremotes`) with `strings.Split` in Go forces the entire output to be converted into a massive slice of strings, incurring significant memory allocation and garbage collection overhead.
**Action:** Use an iterative `for len(stdout) > 0 { line, stdout, _ = strings.Cut(stdout, "\n") }` loop instead. For building arrays, pre-allocate the slice capacity accurately using `strings.Count(stdout, "\n")` to eliminate slice growth overhead.

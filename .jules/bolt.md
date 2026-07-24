## 2024-07-23 - Micro-optimizations in non-hot-path code
**Learning:** Replacing `strings.Split` with `strings.Cut` for command output parsing is considered churn and over-engineering if the code is not on a hot path and operates infrequently on small inputs.
**Action:** Only apply micro-optimizations (like avoiding intermediate slice allocations) in hot paths with demonstrated, measurable benefits. Otherwise, prefer the more readable, idiomatic form.

## 2026-07-24 - Over-optimizing cold paths
**Learning:** Micro-optimizations like using `strings.Cut` instead of `strings.Split` to save memory allocations in string parsing (e.g., rclone output) are considered "churn" and rejected under this project's maintainability policy if they are applied to non-hot-path code with small inputs, as the readability cost outweighs the negligible performance benefit.
**Action:** Always verify if the code being optimized is a true hot path (runs frequently on large inputs) before attempting micro-optimizations that deviate from idiomatic forms.

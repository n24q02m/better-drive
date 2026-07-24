## 2024-07-21 - Rejected strings.Split micro-optimization
**Learning:** Replacing `strings.Split` with iterative `strings.Cut` is considered an over-engineering micro-optimization with readability costs, and will be rejected as churn if not on a hot path.
**Action:** Do not optimize `strings.Split` unless there is a demonstrated, measurable performance issue on a hot path. Idiomatic forms are preferred per maintainability policies.

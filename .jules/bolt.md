## 2023-10-27 - Slice Capacity Pre-allocation
**Learning:** Hidden allocations during `append()` operations are a common performance bottleneck in Go, particularly when parsing and transforming configuration files or filtering arrays.
**Action:** Always pre-allocate slice capacities (e.g., `make([]T, 0, len(input))`) when the required capacity is known upfront or can be reasonably estimated. Additionally, ensure you explicitly handle nil/empty inputs first (e.g., `if len(input) == 0 { return nil }`) if the function contract expects returning `nil` to preserve original behavior.

## 2023-10-27 - Micro-optimizing Single Character Prefix Checks
**Learning:** While `strings.HasPrefix` is generally efficient, for simple single-character checks (e.g. checking for a leading '#' or '!'), zero-allocation byte indexing (e.g., `line[0] == '#'`) is demonstrably faster because it bypasses string length and function call overheads.
**Action:** Replace `strings.HasPrefix(s, "x")` with `s[0] == 'x'` where `x` is a single character, making sure to verify the string is not empty (`s != ""`) beforehand to prevent out-of-bounds panics.

## 2023-10-27 - I/O Latency Dwarfs String Builder Savings
**Learning:** We explored replacing `strings.Join(filters, "\n") + "\n"` with a capacity-preallocated `strings.Builder` loop before passing to `f.WriteString` for large filter configurations. Benchmarks showed `strings.Builder` was faster for pure memory operations, but when combined with the actual disk write (`f.WriteString`), the nanosecond memory allocation savings were completely drowned out by OS I/O latency, making the change an unnecessary complication.
**Action:** Do not sacrifice readability with `strings.Builder` micro-optimizations immediately before blocking I/O (like disk writes) unless the string generation itself is a proven, extreme bottleneck. Stick to the simpler `strings.Join` for routine I/O preparation.

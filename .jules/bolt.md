## 2024-05-24 - Zero-allocation parsing in TranslateIgnoreLines
**Learning:** In high-volume operations like parsing gitignore rules, hidden allocations in `append()` and function call overhead from `strings.HasPrefix` add up.
**Action:** Always pre-allocate slice capacities (`make([]T, 0, len(input))`) and use byte indexing (`str[0] == '#'`) instead of string functions for single-character checks when parsing rules to save a function call.

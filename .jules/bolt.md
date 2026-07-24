## 2025-03-09 - TranslateIgnoreLines Optimization Rejected
**Learning:** Prematurely optimizing cold paths (like parsing `.driveignore` files on small inputs) by replacing standard `strings` functions with manual indexing and slicing violates the "Scalable & Maintainable / no over-engineering" policy. It adds fragility and readability cost for negligible real-world benefit.
**Action:** Do not micro-optimize standard library function calls unless the code is a measured, high-frequency hot path (e.g., tight loop running millions of times per request or rendering cycle).

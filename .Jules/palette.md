## 2026-07-22 - Dynamic Tray State Binding
**Learning:** For a system tray UI in Go using `fyne.io/systray`, tying tooltip updates and menu item availability (enable/disable states) directly into a state change callback (e.g., Aggregator's `OnChange`) significantly improves clarity and prevents users from triggering unsupported actions like trying to manually sync while already paused or syncing.
**Action:** When working with desktop tray apps, bind tooltip strings and dynamic enable/disable controls directly into state observer patterns rather than leaving them static.

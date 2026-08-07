## 2024-05-24 - Dynamic System Tray Feedback
**Learning:** In desktop background applications without a main window, the system tray icon tooltip is a critical, low-friction surface for providing state visibility. Users shouldn't need to open a menu just to see if a sync is active. Furthermore, action items like "Sync now" should reflect current state (e.g., being disabled during a sync) to prevent confusion and duplicate actions.
**Action:** Always bind tray icon tooltips and menu item enabled/disabled states to the application's core state aggregator to provide immediate, passive feedback and prevent impossible actions.

## 2025-02-14 - Contextual Tooltips for Dynamic Menu Items
**Learning:** When system tray menu items change their title (e.g. Pause -> Resume) or become disabled (e.g. Sync now during an active sync), maintaining a static tooltip can lead to conflicting or confusing information for screen readers and sighted users hovering over the item.
**Action:** Always update `SetTooltip` alongside `SetTitle`, `Disable`, or `Enable` calls to ensure the tooltip accurately reflects the current action or state.

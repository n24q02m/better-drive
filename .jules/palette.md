## 2024-05-24 - Dynamic System Tray Feedback
**Learning:** In desktop background applications without a main window, the system tray icon tooltip is a critical, low-friction surface for providing state visibility. Users shouldn't need to open a menu just to see if a sync is active. Furthermore, action items like "Sync now" should reflect current state (e.g., being disabled during a sync) to prevent confusion and duplicate actions.
**Action:** Always bind tray icon tooltips and menu item enabled/disabled states to the application's core state aggregator to provide immediate, passive feedback and prevent impossible actions.

## 2024-08-03 - Dynamic Menu Item Tooltips
**Learning:** In desktop background applications, it's important to keep individual menu item tooltips updated to reflect their dynamic state (like title changes or disabled states). Stale tooltips on disabled or renamed items lead to user confusion and poor accessibility.
**Action:** Always update a menu item's tooltip alongside dynamic changes to its title or disabled state using `SetTooltip`.

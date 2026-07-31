## 2024-05-24 - Dynamic System Tray Feedback
**Learning:** In desktop background applications without a main window, the system tray icon tooltip is a critical, low-friction surface for providing state visibility. Users shouldn't need to open a menu just to see if a sync is active. Furthermore, action items like "Sync now" should reflect current state (e.g., being disabled during a sync) to prevent confusion and duplicate actions.
**Action:** Always bind tray icon tooltips and menu item enabled/disabled states to the application's core state aggregator to provide immediate, passive feedback and prevent impossible actions.

## 2024-08-01 - System Tray Dynamic Tooltips
**Learning:** Adding dynamic tooltips to system tray menu items provides important passive context. For example, explicitly showing "Current status: syncing" or "Sync is currently in progress" when hovering over a disabled button makes the disabled state meaningful rather than confusing.
**Action:** Always provide context for disabled states, specifically by ensuring tooltips dynamically update alongside state changes in desktop/tray applications.

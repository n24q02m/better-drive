## 2024-05-24 - Dynamic System Tray Feedback
**Learning:** In desktop background applications without a main window, the system tray icon tooltip is a critical, low-friction surface for providing state visibility. Users shouldn't need to open a menu just to see if a sync is active. Furthermore, action items like "Sync now" should reflect current state (e.g., being disabled during a sync) to prevent confusion and duplicate actions.
**Action:** Always bind tray icon tooltips and menu item enabled/disabled states to the application's core state aggregator to provide immediate, passive feedback and prevent impossible actions.

## 2024-05-25 - Dynamic Tooltips for Tray Menu Items
**Learning:** Menu items in a system tray should have dynamic tooltips that update according to the application state, providing users with context about what a disabled item means or what an action will do in the current state.
**Action:** Always update tooltips of interactive elements during dynamic state transitions, not just their enabled/disabled state.

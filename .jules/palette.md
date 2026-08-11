## 2024-05-24 - Dynamic System Tray Feedback
**Learning:** In desktop background applications without a main window, the system tray icon tooltip is a critical, low-friction surface for providing state visibility. Users shouldn't need to open a menu just to see if a sync is active. Furthermore, action items like "Sync now" should reflect current state (e.g., being disabled during a sync) to prevent confusion and duplicate actions.
**Action:** Always bind tray icon tooltips and menu item enabled/disabled states to the application's core state aggregator to provide immediate, passive feedback and prevent impossible actions.

## 2024-05-25 - Contextual Menu Item Tooltips
**Learning:** When dynamically changing a system tray menu item's text or disabled state (like swapping 'Pause' to 'Resume'), leaving the original tooltip unchanged causes accessibility and UX friction, as the tooltip contradicts the visible text or fails to explain the disabled state.
**Action:** Always update a menu item's tooltip (`SetTooltip`) alongside dynamic changes to its title or disabled state to preserve context and ensure accessibility.

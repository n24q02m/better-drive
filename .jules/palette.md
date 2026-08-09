## 2024-05-24 - Dynamic System Tray Feedback
**Learning:** In desktop background applications without a main window, the system tray icon tooltip is a critical, low-friction surface for providing state visibility. Users shouldn't need to open a menu just to see if a sync is active. Furthermore, action items like "Sync now" should reflect current state (e.g., being disabled during a sync) to prevent confusion and duplicate actions.
**Action:** Always bind tray icon tooltips and menu item enabled/disabled states to the application's core state aggregator to provide immediate, passive feedback and prevent impossible actions.

## 2024-05-25 - Dynamic Tooltips for Menu Items
**Learning:** When dynamically changing a menu item's title or disabled state in a system tray application, the tooltip must also be updated to preserve context and ensure accessibility. A disabled "Sync now" button without a tooltip leaves users confused about why it's disabled.
**Action:** Always update a menu item's tooltip (`SetTooltip`) alongside dynamic changes to its title or disabled state to provide context.

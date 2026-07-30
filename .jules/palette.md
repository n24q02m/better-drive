## 2024-05-24 - Dynamic System Tray Feedback
**Learning:** In desktop background applications without a main window, the system tray icon tooltip is a critical, low-friction surface for providing state visibility. Users shouldn't need to open a menu just to see if a sync is active. Furthermore, action items like "Sync now" should reflect current state (e.g., being disabled during a sync) to prevent confusion and duplicate actions.
**Action:** Always bind tray icon tooltips and menu item enabled/disabled states to the application's core state aggregator to provide immediate, passive feedback and prevent impossible actions.

## 2024-05-24 - Contextual System Tray Tooltips
**Learning:** When system tray menu items change state (like toggling between Pause and Resume, or becoming disabled during an active operation), just changing the label or disabling it isn't enough for optimal UX and accessibility. Screen readers and users hovering over disabled items need to understand *why* the item is in its current state or what it will do next.
**Action:** Always update a menu item's tooltip (`SetTooltip`) simultaneously with any dynamic changes to its title or disabled state to preserve context and ensure accessibility in systray UI implementations.

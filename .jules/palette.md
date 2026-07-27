## 2024-05-24 - Dynamic System Tray Feedback
**Learning:** In desktop background applications without a main window, the system tray icon tooltip is a critical, low-friction surface for providing state visibility. Users shouldn't need to open a menu just to see if a sync is active. Furthermore, action items like "Sync now" should reflect current state (e.g., being disabled during a sync) to prevent confusion and duplicate actions.
**Action:** Always bind tray icon tooltips and menu item enabled/disabled states to the application's core state aggregator to provide immediate, passive feedback and prevent impossible actions.
## 2024-05-18 - Dynamic Tooltips for Tray Menu Items
**Learning:** Tray icon menu items in `fyne.io/systray` can feel unresponsive or confusing if their tooltips don't update to reflect their disabled state or their action changing based on background context (e.g., 'Pause' becoming 'Resume').
**Action:** When updating the title and disabled states of tray menu items dynamically, also bind and update their tooltips inside the state aggregator callback using `MenuItem.SetTooltip()`.

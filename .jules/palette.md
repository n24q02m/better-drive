## 2024-05-24 - Dynamic System Tray Feedback
**Learning:** In desktop background applications without a main window, the system tray icon tooltip is a critical, low-friction surface for providing state visibility. Users shouldn't need to open a menu just to see if a sync is active. Furthermore, action items like "Sync now" should reflect current state (e.g., being disabled during a sync) to prevent confusion and duplicate actions.
**Action:** Always bind tray icon tooltips and menu item enabled/disabled states to the application's core state aggregator to provide immediate, passive feedback and prevent impossible actions.
## 2026-07-28 - Update system tray menu item tooltips dynamically
**Learning:** System tray UI tooltips must be updated synchronously with their titles and disabled states (e.g. Pause/Resume, Enable/Disable) to maintain accessibility and correct context.
**Action:** When updating dynamic state for tray UI components, ensure to also call `SetTooltip` with appropriate context.

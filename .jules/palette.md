## 2024-05-24 - Dynamic System Tray Feedback
**Learning:** In desktop background applications without a main window, the system tray icon tooltip is a critical, low-friction surface for providing state visibility. Users shouldn't need to open a menu just to see if a sync is active. Furthermore, action items like "Sync now" should reflect current state (e.g., being disabled during a sync) to prevent confusion and duplicate actions.
**Action:** Always bind tray icon tooltips and menu item enabled/disabled states to the application's core state aggregator to provide immediate, passive feedback and prevent impossible actions.

## 2026-07-29 - Dynamic Menu Item Tooltips
**Learning:** For desktop application tray menus, static tooltips can become misleading when the state or title of a menu item changes (e.g., a "Pause" item becoming "Resume"). Similarly, a disabled action (like "Sync now" during an active sync) needs a tooltip explaining *why* it is disabled, which a static tooltip fails to provide. Providing contextual, dynamic tooltips ensures screen readers and hover states give users accurate, accessible feedback.
**Action:** Whenever dynamically updating a UI element's text or disabled state in `fyne.io/systray` (or similar frameworks), always update its associated tooltip (`SetTooltip`) simultaneously.

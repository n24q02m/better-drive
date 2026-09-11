## 2024-05-24 - Dynamic System Tray Feedback
**Learning:** In desktop background applications without a main window, the system tray icon tooltip is a critical, low-friction surface for providing state visibility. Users shouldn't need to open a menu just to see if a sync is active. Furthermore, action items like "Sync now" should reflect current state (e.g., being disabled during a sync) to prevent confusion and duplicate actions.
**Action:** Always bind tray icon tooltips and menu item enabled/disabled states to the application's core state aggregator to provide immediate, passive feedback and prevent impossible actions.

## 2024-08-03 - Dynamic Menu Item Tooltips
**Learning:** In desktop background applications, it's important to keep individual menu item tooltips updated to reflect their dynamic state (like title changes or disabled states). Stale tooltips on disabled or renamed items lead to user confusion and poor accessibility.
**Action:** Always update a menu item's tooltip alongside dynamic changes to its title or disabled state using `SetTooltip`.

## 2024-05-25 - Contextual Menu Item Tooltips
**Learning:** When dynamically changing a system tray menu item's text or disabled state (like swapping 'Pause' to 'Resume'), leaving the original tooltip unchanged causes accessibility and UX friction, as the tooltip contradicts the visible text or fails to explain the disabled state.
**Action:** Always update a menu item's tooltip (`SetTooltip`) alongside dynamic changes to its title or disabled state to preserve context and ensure accessibility.
## 2024-08-11 - Prevent silent failure on paused sync

**Learning:** Exposing actionable UI elements (like a "Sync now" button) that silently fail because of another system state (like being Paused) causes user confusion. It's an accessibility and interaction issue where the system doesn't communicate its constraints.

**Action:** Always disable contextual actions when they are made invalid by another system state, and update their tooltip to explain *why* they are disabled (e.g., "Cannot sync while paused").
## 2024-05-24 - Enable 'Open folder' for non-Windows platforms
**Learning:** Hardcoding desktop UI interactions to specific platforms (e.g., Windows 'explorer') artificially limits the application's usability and causes silent failures on macOS and Linux, harming cross-platform UX.
**Action:** When implementing cross-platform UI features like 'open folder', always provide the standard OS-specific command (`open` for macOS, `xdg-open` for Linux) to ensure a consistent experience.
## 2024-05-24 - Cross-platform UI handling
**Learning:** A PR to enable 'Open folder' on macOS and Linux was superseded by another PR (#189) that handled the same cluster of duplicate functionality.
**Action:** Always check if a similar PR already exists or if someone else is working on the same area to avoid duplicate effort.

## 2024-10-24 - Systray Tooltip Accessibility
**Learning:** In desktop tray apps using `fyne.io/systray`, dynamically changing a menu item's title or disabled state without updating its tooltip creates an accessibility gap where screen readers or hover states provide stale context.
**Action:** Always update a menu item's tooltip (`SetTooltip`) alongside any dynamic changes to its title or enabled/disabled state to preserve accurate context.

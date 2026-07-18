## 2026-07-18 - Added separators to system tray menu
**Learning:** System tray menus can become cluttered; grouping logical items with separators improves interaction safety (e.g. separating Quit from other actions).
**Action:** Always check if a system tray menu has logical groups and use `systray.AddSeparator()` to visually differentiate them.

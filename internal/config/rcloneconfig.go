package config

import (
	"os"
	"path/filepath"
)

type candidate struct {
	root string
	path string
}

// ResolveRcloneConfig returns the rclone config path to hand librclone: the
// explicit value if non-empty, else the first existing default location. The
// scoop portable rclone.conf (adjacent to the scoop rclone binary) is only
// found by the rclone CLI, not by librclone's default (%APPDATA%), so probe it
// first, then %APPDATA%. Returns "" if none exist (librclone falls back to its
// own default).
func ResolveRcloneConfig(explicit string) string {
	if explicit != "" {
		return explicit
	}

	home, _ := os.UserHomeDir()
	candidates := []candidate{
		{home, "scoop/apps/rclone/current/rclone.conf"},
	}
	if ad := os.Getenv("APPDATA"); ad != "" {
		candidates = append(candidates, candidate{ad, "rclone/rclone.conf"})
	}

	for _, c := range candidates {
		if root, err := os.OpenRoot(c.root); err == nil {
			if fi, err := root.Stat(c.path); err == nil && !fi.IsDir() {
				_ = root.Close() // #nosec G104 -- handle thư mục chỉ đọc; lỗi đóng không có hành động xử lý khả dụng.
				return filepath.Join(c.root, filepath.FromSlash(c.path))
			}
			_ = root.Close() // #nosec G104 -- handle thư mục chỉ đọc; lỗi đóng không có hành động xử lý khả dụng.
		}
	}
	return ""
}

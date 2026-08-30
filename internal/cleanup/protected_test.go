package cleanup

import (
	"path/filepath"
	"testing"

	"github.com/n24q02m/better-drive/internal/protectedfs"
)

func protectedTestDir(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "authority.git")
	if err := protectedfs.EnsurePrivateDir(path); err != nil {
		t.Fatal(err)
	}
	return path
}

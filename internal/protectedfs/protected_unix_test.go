//go:build !windows

package protectedfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenPrivateFileRejectsSymlinkAndPermissiveMode(t *testing.T) {
	root := filepath.Join(t.TempDir(), "protected")
	if err := EnsurePrivateDir(root); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	file, err := CreatePrivateFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("secret"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if file, err := OpenPrivateFile(link); err == nil {
		_ = file.Close()
		t.Fatal("opened protected file through a symlink")
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	if file, err := OpenPrivateFile(target); err == nil {
		_ = file.Close()
		t.Fatal("opened protected file with group/world permissions")
	}
}

func TestEnsurePrivateDirDoesNotChmodSymlinkTarget(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDir(link); err == nil {
		t.Fatal("accepted a symlink as a protected directory")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("symlink target permissions = %o, want unchanged 755", info.Mode().Perm())
	}
}

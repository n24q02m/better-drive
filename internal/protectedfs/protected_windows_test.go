//go:build windows

package protectedfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateDirectoryAndFileUseProtectedOwnerOnlyDACLs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cleanup-security")
	if err := EnsurePrivateDir(root); err != nil {
		t.Fatalf("create protected directory: %v", err)
	}
	path := filepath.Join(root, "trust-bundle.json")
	file, err := CreatePrivateFile(path)
	if err != nil {
		t.Fatalf("create protected file: %v", err)
	}
	if _, err := file.WriteString("{}\n"); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPrivateFile(file); err != nil {
		t.Fatalf("verify protected file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenPrivateFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := VerifyPrivateFile(reopened); err != nil {
		t.Fatalf("verify reopened protected file: %v", err)
	}
}

func TestPrivateFileRejectsInheritedPermissiveDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ordinary.txt")
	if err := os.WriteFile(path, []byte("ordinary"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := VerifyPrivateFile(file); err == nil {
		t.Fatal("ordinary inherited DACL was accepted as protected")
	}
}

func TestOpenPrivateFileRejectsReparsePoint(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cleanup-security")
	if err := EnsurePrivateDir(root); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	file, err := CreatePrivateFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("file symlinks are unavailable: %v", err)
	}
	if file, err := OpenPrivateFile(link); err == nil {
		_ = file.Close()
		t.Fatal("opened protected file through a reparse point")
	}
}

package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/rclone/rclone/fs/filter"
)

func TestTranslateDriveIgnore(t *testing.T) {
	root := t.TempDir()
	body := "# comment\n\nnode_modules/\n*.tmp\n/build\n!keep.tmp\n"
	if err := os.WriteFile(filepath.Join(root, ".driveignore"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := TranslateDriveIgnore(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"+ keep.tmp",
		"- /build",
		"- *.tmp",
		"- node_modules/**",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %#v\nwant %#v", got, want)
	}
}

func TestTranslateDriveIgnoreMissingFile(t *testing.T) {
	got, err := TranslateDriveIgnore(t.TempDir())
	if err != nil || got != nil {
		t.Fatalf("missing file: got=%v err=%v, want nil,nil", got, err)
	}
}

func TestTranslateDriveIgnoreAnchoring(t *testing.T) {
	root := t.TempDir()
	body := "a/b\nfoo/bar/\n"
	if err := os.WriteFile(filepath.Join(root, ".driveignore"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := TranslateDriveIgnore(root)
	if err != nil {
		t.Fatal(err)
	}
	// middle-slash patterns anchor to root ("/..."); reversed order (last line first).
	want := []string{"- /foo/bar/**", "- /a/b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %#v\nwant %#v", got, want)
	}
}

// TestTranslateDriveIgnoreAgainstRealRcloneFilter builds an actual rclone
// fs/filter.Filter from the translated rules to guard against regressions
// that only rclone's own filter compiler (not our string assertions) would
// catch (e.g. anchoring or precedence mistakes).
func TestTranslateDriveIgnoreAgainstRealRcloneFilter(t *testing.T) {
	root := t.TempDir()
	body := "*.tmp\n!keep.txt\n"
	if err := os.WriteFile(filepath.Join(root, ".driveignore"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	rules, err := TranslateDriveIgnore(root)
	if err != nil {
		t.Fatal(err)
	}

	f, err := filter.NewFilter(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rules {
		if err := f.AddRule(r); err != nil {
			t.Fatalf("AddRule(%q): %v", r, err)
		}
	}

	now := time.Now()
	if f.Include("skip.tmp", 0, now, nil) {
		t.Error("skip.tmp at root should be excluded by *.tmp")
	}
	if !f.Include("keep.txt", 0, now, nil) {
		t.Error("keep.txt at root should be included")
	}
}

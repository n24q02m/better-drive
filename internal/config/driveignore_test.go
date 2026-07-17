package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
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
		"- **/node_modules/**",
		"- **/*.tmp",
		"- build",
		"+ **/keep.tmp",
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

package config

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
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

// TestPairFiltersExcludeOnlyNoFile verifies PairFilters translates a pair's
// config-level Exclude list correctly when localRoot has no .driveignore
// file at all (the replace-backup-script use case: excludes live entirely in
// config, nothing is ever written into the real directory).
func TestPairFiltersExcludeOnlyNoFile(t *testing.T) {
	root := t.TempDir() // no .driveignore written here
	exclude := []string{"node_modules/", "*.tmp", "/build"}
	got, err := PairFilters(root, exclude)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"- /build", "- *.tmp", "- node_modules/**"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %#v\nwant %#v", got, want)
	}
}

// TestPairFiltersCombinesExcludeAndFile verifies PairFilters combines the
// pair's Exclude list (checked first, i.e. added earlier) with the
// .driveignore file's lines (added after), matching gitignore's
// last-match-wins semantics: a later negation in the .driveignore file must
// still be able to override an earlier config-level exclude.
func TestPairFiltersCombinesExcludeAndFile(t *testing.T) {
	root := t.TempDir()
	body := "!keep.tmp\n"
	if err := os.WriteFile(filepath.Join(root, ".driveignore"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := PairFilters(root, []string{"*.tmp"})
	if err != nil {
		t.Fatal(err)
	}
	// combined line order (gitignore semantics): "*.tmp" (exclude, config)
	// then "!keep.tmp" (file) - file wins because it comes later, so after
	// the whole-list reversal the negation is checked first.
	want := []string{"+ keep.tmp", "- *.tmp"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %#v\nwant %#v", got, want)
	}
}

// TestPairFiltersTableDriven exercises PairFilters across several
// exclude/file combinations, reusing the same translation rules already
// covered by TestTranslateDriveIgnore/TestTranslateDriveIgnoreAnchoring.
func TestPairFiltersTableDriven(t *testing.T) {
	cases := []struct {
		name        string
		exclude     []string
		driveignore string // empty means: do not write the file at all
		want        []string
	}{
		{
			name:    "exclude_only",
			exclude: []string{".venv/", "__pycache__/"},
			want:    []string{"- __pycache__/**", "- .venv/**"},
		},
		{
			name:        "file_only_no_exclude",
			driveignore: "*.log\n",
			want:        []string{"- *.log"},
		},
		{
			name:        "both_no_overlap",
			exclude:     []string{".git/"},
			driveignore: "*.log\n",
			want:        []string{"- *.log", "- .git/**"},
		},
		{
			name:    "empty_exclude_no_file",
			exclude: []string{},
			want:    nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.driveignore != "" {
				if err := os.WriteFile(filepath.Join(root, ".driveignore"), []byte(tc.driveignore), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := PairFilters(root, tc.exclude)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got  %#v\nwant %#v", got, tc.want)
			}
		})
	}
}

// TestPairFiltersNeverWritesDriveignoreFile verifies PairFilters is
// read-only with respect to localRoot: it must never create/write a
// .driveignore file there (the whole point of config-level Exclude is to
// avoid touching real user directories like ~/.claude).
func TestPairFiltersNeverWritesDriveignoreFile(t *testing.T) {
	root := t.TempDir()
	if _, err := PairFilters(root, []string{"*.tmp"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".driveignore")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("PairFilters must not create .driveignore in localRoot; stat err=%v", err)
	}
}

// TestTranslateDriveIgnoreAgainstRealRcloneFilter shells out to the system
// rclone binary (`rclone lsf --filter-from`) to verify the translated rules
// against rclone's OWN filter engine, not just our string assertions - a
// guard against regressions (e.g. anchoring or precedence mistakes) that only
// rclone's real compiler would catch. Skipped when rclone is not on PATH
// (e.g. GitHub-hosted CI runners, which do not preinstall it): a real rclone
// binary is a hard runtime dependency of better-drive itself (see
// internal/engine), so this only skips an unavailable-in-CI check, not an
// unavailable-in-production one.
func TestTranslateDriveIgnoreAgainstRealRcloneFilter(t *testing.T) {
	bin, err := exec.LookPath("rclone")
	if err != nil {
		t.Skip("rclone not found on PATH, skipping real-filter regression check")
	}

	root := t.TempDir()
	body := "*.tmp\n!keep.txt\n"
	if err := os.WriteFile(filepath.Join(root, ".driveignore"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	rules, err := TranslateDriveIgnore(root)
	if err != nil {
		t.Fatal(err)
	}
	filterFile := filepath.Join(t.TempDir(), "filter.txt")
	if err := os.WriteFile(filterFile, []byte(strings.Join(rules, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A separate directory (root only holds .driveignore) to list through the
	// translated filter.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "skip.tmp"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "lsf", "--filter-from", filterFile, dir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("rclone lsf: %v: %s", err, stderr.String())
	}
	got := stdout.String()
	if strings.Contains(got, "skip.tmp") {
		t.Errorf("rclone lsf output %q: skip.tmp should be excluded by *.tmp", got)
	}
	if !strings.Contains(got, "keep.txt") {
		t.Errorf("rclone lsf output %q: keep.txt should be included", got)
	}
}

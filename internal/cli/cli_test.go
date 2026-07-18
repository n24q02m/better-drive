package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRootHasSubcommands(t *testing.T) {
	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"setup", "run", "status"} {
		if !bytes.Contains(buf.Bytes(), []byte(sub)) {
			t.Errorf("help missing subcommand %q", sub)
		}
	}
}

// TestStatusCmdPrintsAllPairs verifies `better-drive status` with a
// multi-pair config prints one "pair: ..." line per [[pair]] block (not just
// the first, as the pre-multi-pair implementation did with Pairs[0]).
// paths.ConfigFile() resolves under os.UserConfigDir(), which on Windows
// reads the AppData env var - t.Setenv redirects it to a throwaway dir so
// this never touches a real user config.
func TestStatusCmdPrintsAllPairs(t *testing.T) {
	appData := t.TempDir()
	t.Setenv("AppData", appData)

	cfgDir := filepath.Join(appData, "better-drive")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `
[[pair]]
local = "C:/pair0"
remote = "gdrive:pair0"
interval = "30s"

[[pair]]
local = "C:/pair1"
remote = "gdrive:pair1"
interval = "1m"
mode = "copy"
exclude = ["node_modules/"]
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newRootCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"status"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status: %v", err)
	}

	out := buf.String()
	for _, want := range []string{"C:/pair0", "gdrive:pair0", "[mode=bisync]", "C:/pair1", "gdrive:pair1", "[mode=copy]"} {
		if !bytes.Contains([]byte(out), []byte(want)) {
			t.Errorf("status output missing %q; got:\n%s", want, out)
		}
	}
}

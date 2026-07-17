package cli

import (
	"bytes"
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

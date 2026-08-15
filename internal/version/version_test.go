package version

import "testing"

func TestDefaultBuildMetadataIsPresent(t *testing.T) {
	if Version == "" || Commit == "" || Date == "" {
		t.Fatalf("build metadata must be non-empty: version=%q commit=%q date=%q", Version, Commit, Date)
	}
}

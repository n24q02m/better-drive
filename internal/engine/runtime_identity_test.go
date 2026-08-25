package engine

import (
	"strings"
	"testing"
)

func TestCompareRuntimeChildImageRejectsPathMismatch(t *testing.T) {
	expected := runtimeFileEvidence{identity: "identity-1", acl: "acl-1"}
	actual := runtimeChildImage{path: `C:\Program Files\rclone\other.exe`, identity: expected.identity}
	if err := compareRuntimeChildImage(`C:\Program Files\rclone\rclone.exe`, expected.identity, actual); err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("compareRuntimeChildImage path error = %v, want path mismatch", err)
	}
}

func TestCompareRuntimeChildImageRejectsIdentityMismatch(t *testing.T) {
	expected := runtimeFileEvidence{identity: "identity-1", acl: "acl-1"}
	actual := runtimeChildImage{path: `C:\Program Files\rclone\rclone.exe`, identity: "identity-2"}
	if err := compareRuntimeChildImage(actual.path, expected.identity, actual); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("compareRuntimeChildImage identity error = %v, want identity mismatch", err)
	}
}

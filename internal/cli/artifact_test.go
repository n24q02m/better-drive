package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n24q02m/better-drive/internal/artifactcrypto"
)

func TestArtifactSealAndOpenRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	ref := artifactcrypto.KeyReference{ID: "test-key", Version: 1}
	resolver := cliArtifactResolver{ref: key}

	inPath := filepath.Join(t.TempDir(), "plaintext.bin")
	sealedPath := filepath.Join(t.TempDir(), "sealed.art")
	outPath := filepath.Join(t.TempDir(), "restored.bin")

	payload := []byte("confidential-backup-data-payload-12345")
	if err := os.WriteFile(inPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	// Seal
	sealCmd := artifactCmdWithResolver(resolver)
	var sealOut bytes.Buffer
	sealCmd.SetOut(&sealOut)
	sealCmd.SetErr(&bytes.Buffer{})
	sealCmd.SetArgs([]string{
		"seal",
		"--key-ref", "test-key",
		"--key-version", "1",
		"--restore-set-id", "set-1",
		"--component", "db",
		"--input", inPath,
		"--output", sealedPath,
		"--format", "json",
	})
	if err := sealCmd.Execute(); err != nil {
		t.Fatalf("artifact seal: %v", err)
	}

	// Verify seal JSON output does NOT expose plaintext digest
	sealJSON := sealOut.String()
	if strings.Contains(sealJSON, "plaintext_digest") {
		t.Fatalf("artifact seal output must not expose plaintext digest: %s", sealJSON)
	}
	if !strings.Contains(sealJSON, "ciphertext_digest") {
		t.Fatalf("artifact seal output missing ciphertext_digest: %s", sealJSON)
	}

	// Open
	openCmd := artifactCmdWithResolver(resolver)
	var openOut bytes.Buffer
	openCmd.SetOut(&openOut)
	openCmd.SetErr(&bytes.Buffer{})
	openCmd.SetArgs([]string{
		"open",
		"--key-ref", "test-key",
		"--key-version", "1",
		"--restore-set-id", "set-1",
		"--component", "db",
		"--input", sealedPath,
		"--output", outPath,
		"--format", "json",
	})
	if err := openCmd.Execute(); err != nil {
		t.Fatalf("artifact open: %v", err)
	}

	restored, err := os.ReadFile(outPath)
	if err != nil || !bytes.Equal(restored, payload) {
		t.Fatalf("restored payload = %q, want %q, err=%v", restored, payload, err)
	}
}

func TestArtifactOpenRejectsTamperedCiphertext(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	ref := artifactcrypto.KeyReference{ID: "test-key", Version: 1}
	resolver := cliArtifactResolver{ref: key}

	inPath := filepath.Join(t.TempDir(), "plaintext.bin")
	sealedPath := filepath.Join(t.TempDir(), "sealed.art")
	outPath := filepath.Join(t.TempDir(), "restored.bin")

	payload := []byte("confidential-backup-data")
	if err := os.WriteFile(inPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	sealCmd := artifactCmdWithResolver(resolver)
	sealCmd.SetOut(&bytes.Buffer{})
	sealCmd.SetErr(&bytes.Buffer{})
	sealCmd.SetArgs([]string{
		"seal",
		"--key-ref", "test-key",
		"--key-version", "1",
		"--restore-set-id", "set-1",
		"--component", "db",
		"--input", inPath,
		"--output", sealedPath,
	})
	if err := sealCmd.Execute(); err != nil {
		t.Fatalf("artifact seal: %v", err)
	}

	// Tamper ciphertext
	data, err := os.ReadFile(sealedPath)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 0xFF
	if err := os.WriteFile(sealedPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	openCmd := artifactCmdWithResolver(resolver)
	openCmd.SetOut(&bytes.Buffer{})
	openCmd.SetErr(&bytes.Buffer{})
	openCmd.SetArgs([]string{
		"open",
		"--key-ref", "test-key",
		"--key-version", "1",
		"--restore-set-id", "set-1",
		"--component", "db",
		"--input", sealedPath,
		"--output", outPath,
	})
	if err := openCmd.Execute(); err == nil {
		t.Fatal("artifact open accepted tampered ciphertext")
	}
	if _, err := os.Lstat(outPath); !os.IsNotExist(err) {
		t.Fatal("tampered artifact open created output file")
	}
}

func TestArtifactRequiresResolverAndMetadata(t *testing.T) {
	cmd := artifactCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"seal", "--key-ref", "k", "--restore-set-id", "s", "--component", "c"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "resolver") {
		t.Fatalf("artifact seal without resolver error = %v, want resolver required", err)
	}

	resolver := cliArtifactResolver{}
	cmd2 := artifactCmdWithResolver(resolver)
	cmd2.SetOut(&bytes.Buffer{})
	cmd2.SetErr(&bytes.Buffer{})
	cmd2.SetArgs([]string{"seal"})
	if err := cmd2.Execute(); err == nil || !strings.Contains(err.Error(), "requires") {
		t.Fatalf("artifact seal without metadata error = %v, want metadata required", err)
	}
}

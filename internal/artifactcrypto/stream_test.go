package artifactcrypto

import (
	"bytes"
	"strings"
	"testing"
)

func TestSealOpenRoundTripBindsMetadataAndDigest(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	plaintext := bytes.Repeat([]byte("restore-data-"), 10000)
	metadata := Metadata{RestoreSetID: "set-1", Component: "state", KeyRef: "key:v1"}
	var sealed bytes.Buffer
	result, err := Seal(&sealed, bytes.NewReader(plaintext), key, metadata)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if result.PlaintextDigest == "" || result.CiphertextDigest == "" {
		t.Fatalf("Seal result = %#v, want both digests", result)
	}
	var opened bytes.Buffer
	if err := Open(&opened, bytes.NewReader(sealed.Bytes()), key, metadata); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(opened.Bytes(), plaintext) {
		t.Fatal("opened plaintext differs from sealed input")
	}
}

func TestOpenRejectsTamperTruncationWrongKeyAndMetadataDrift(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	metadata := Metadata{RestoreSetID: "set-1", Component: "state", KeyRef: "key:v1"}
	var sealed bytes.Buffer
	if _, err := Seal(&sealed, strings.NewReader("secret payload"), key, metadata); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		data []byte
		key  []byte
		meta Metadata
	}{
		{name: "tamper", data: append([]byte(nil), sealed.Bytes()[:len(sealed.Bytes())-1]...), key: key, meta: metadata},
		{name: "wrong key", data: sealed.Bytes(), key: []byte("abcdef0123456789abcdef0123456789"), meta: metadata},
		{name: "metadata drift", data: sealed.Bytes(), key: key, meta: Metadata{RestoreSetID: "other", Component: "state", KeyRef: "key:v1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := Open(&out, bytes.NewReader(tc.data), tc.key, tc.meta); err == nil {
				t.Fatal("Open succeeded, want fail-closed rejection")
			}
		})
	}
}

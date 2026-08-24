package artifactcrypto

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
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

func TestOpenRejectsOversizedFrameBeforeReadingCiphertext(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	metadata := Metadata{RestoreSetID: "set-1", Component: "state", KeyRef: "key:v1"}
	var sealed bytes.Buffer
	if _, err := Seal(&sealed, strings.NewReader("secret payload"), key, metadata); err != nil {
		t.Fatal(err)
	}
	data := append([]byte(nil), sealed.Bytes()...)
	headerLen := binary.BigEndian.Uint32(data[len(magic) : len(magic)+4])
	frameOffset := len(magic) + 4 + int(headerLen)
	plainLenOffset := frameOffset + 1 + 8
	cipherLenOffset := plainLenOffset + 4
	binary.BigEndian.PutUint32(data[plainLenOffset:plainLenOffset+4], chunkSize+1)
	binary.BigEndian.PutUint32(data[cipherLenOffset:cipherLenOffset+4], chunkSize+1+16)

	var out bytes.Buffer
	err := Open(&out, bytes.NewReader(data), key, metadata)
	if err == nil || !strings.Contains(err.Error(), "frame header") {
		t.Fatalf("Open error = %v, want oversized-frame header rejection", err)
	}
}

type testResolver map[KeyReference][]byte

func (r testResolver) Resolve(reference KeyReference) ([]byte, error) {
	key, ok := r[reference]
	if !ok {
		return nil, fmt.Errorf("missing key %s@%d", reference.ID, reference.Version)
	}
	return append([]byte(nil), key...), nil
}

func TestSealOpenResolvesTypedReferenceAndNeverSerializesKey(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	metadata := Metadata{RestoreSetID: "set-typed", Component: "state", KeyRef: "drive-state", KeyVersion: 7}
	resolver := testResolver{metadata.Reference(): key}
	var sealed bytes.Buffer
	if _, err := Seal(&sealed, strings.NewReader("secret payload"), resolver, metadata); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(sealed.Bytes(), key) {
		t.Fatal("sealed artifact serialized key material")
	}
	var opened bytes.Buffer
	if err := Open(&opened, bytes.NewReader(sealed.Bytes()), resolver, metadata); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened.String() != "secret payload" {
		t.Fatalf("opened plaintext = %q", opened.String())
	}
}

func TestOpenRejectsWrongReferenceVersionAndLeavesDestinationUnchanged(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	metadata := Metadata{RestoreSetID: "set-typed", Component: "state", KeyRef: "drive-state", KeyVersion: 7}
	resolver := testResolver{metadata.Reference(): key}
	var sealed bytes.Buffer
	if _, err := Seal(&sealed, strings.NewReader("secret payload"), resolver, metadata); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []Metadata{
		{RestoreSetID: metadata.RestoreSetID, Component: metadata.Component, KeyRef: metadata.KeyRef, KeyVersion: 8},
		{RestoreSetID: metadata.RestoreSetID, Component: metadata.Component, KeyRef: metadata.KeyRef + "-other", KeyVersion: metadata.KeyVersion},
	} {
		var out bytes.Buffer
		out.WriteString("unchanged")
		if err := Open(&out, bytes.NewReader(sealed.Bytes()), resolver, expected); err == nil {
			t.Fatal("Open accepted wrong key reference/version")
		}
		if out.String() != "unchanged" {
			t.Fatalf("destination changed after key metadata rejection: %q", out.String())
		}
	}
}

type interruptedReader struct {
	read bool
}

func (r *interruptedReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, errors.New("interrupted")
	}
	r.read = true
	copy(p, []byte("partial"))
	return len("partial"), nil
}

func TestSealAndOpenFailuresLeaveDestinationUnchanged(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	metadata := Metadata{RestoreSetID: "set-typed", Component: "state", KeyRef: "drive-state", KeyVersion: 7}
	resolver := testResolver{metadata.Reference(): key}
	var sealed bytes.Buffer
	if _, err := Seal(&sealed, strings.NewReader("secret payload"), resolver, metadata); err != nil {
		t.Fatal(err)
	}
	var sealOut bytes.Buffer
	sealOut.WriteString("unchanged")
	if _, err := Seal(&sealOut, &interruptedReader{}, resolver, metadata); err == nil {
		t.Fatal("Seal accepted interrupted source")
	}
	if sealOut.String() != "unchanged" {
		t.Fatalf("Seal wrote residue after source interruption: %q", sealOut.String())
	}
	data := append([]byte(nil), sealed.Bytes()...)
	data[len(data)-1] ^= 0x01
	var openOut bytes.Buffer
	openOut.WriteString("unchanged")
	if err := Open(&openOut, bytes.NewReader(data), resolver, metadata); err == nil {
		t.Fatal("Open accepted tampered footer")
	}
	if openOut.String() != "unchanged" {
		t.Fatalf("Open wrote residue after footer tamper: %q", openOut.String())
	}
}

package artifactcrypto

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

var (
	_ Resolver                                                           = ResolverFunc(nil)
	_ func(io.Writer, io.Reader, Resolver, Metadata) (SealResult, error) = Seal
	_ func(io.Writer, io.Reader, Resolver, Metadata) error               = Open
)

func testKey() []byte {
	return []byte("0123456789abcdef0123456789abcdef")
}

func testMetadata() Metadata {
	return Metadata{
		RestoreSetID: "set-1",
		Component:    "state",
		KeyRef:       "key:v1",
		KeyVersion:   1,
	}
}

func TestSealOpenRoundTripBindsMetadataAndDigest(t *testing.T) {
	key := testKey()
	metadata := testMetadata()
	resolver := testResolver{metadata.Reference(): key}
	plaintext := bytes.Repeat([]byte("restore-data-"), 10000)
	var sealed bytes.Buffer
	result, err := Seal(&sealed, bytes.NewReader(plaintext), resolver, metadata)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if result.PlaintextDigest == "" || result.CiphertextDigest == "" {
		t.Fatalf("Seal result = %#v, want both digests", result)
	}
	var opened bytes.Buffer
	if err := Open(&opened, bytes.NewReader(sealed.Bytes()), resolver, metadata); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(opened.Bytes(), plaintext) {
		t.Fatal("opened plaintext differs from sealed input")
	}
}

func TestOpenRejectsTamperTruncationWrongKeyAndMetadataDrift(t *testing.T) {
	key := testKey()
	metadata := testMetadata()
	resolver := testResolver{metadata.Reference(): key}
	var sealed bytes.Buffer
	if _, err := Seal(&sealed, strings.NewReader("secret payload"), resolver, metadata); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		data     []byte
		resolver Resolver
		meta     Metadata
	}{
		{
			name:     "tamper",
			data:     append([]byte(nil), sealed.Bytes()[:len(sealed.Bytes())-1]...),
			resolver: resolver,
			meta:     metadata,
		},
		{
			name: "wrong key",
			data: sealed.Bytes(),
			resolver: testResolver{
				metadata.Reference(): []byte("abcdef0123456789abcdef0123456789"),
			},
			meta: metadata,
		},
		{
			name:     "metadata drift",
			data:     sealed.Bytes(),
			resolver: resolver,
			meta:     Metadata{RestoreSetID: "other", Component: "state", KeyRef: "key:v1", KeyVersion: 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := Open(&out, bytes.NewReader(tc.data), tc.resolver, tc.meta); err == nil {
				t.Fatal("Open succeeded, want fail-closed rejection")
			}
			if out.Len() != 0 {
				t.Fatalf("destination changed after rejection: %q", out.String())
			}
		})
	}
}

func TestOpenRejectsOversizedFrameBeforeReadingCiphertext(t *testing.T) {
	key := testKey()
	metadata := testMetadata()
	resolver := testResolver{metadata.Reference(): key}
	var sealed bytes.Buffer
	if _, err := Seal(&sealed, strings.NewReader("secret payload"), resolver, metadata); err != nil {
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
	err := Open(&out, bytes.NewReader(data), resolver, metadata)
	if err == nil || !strings.Contains(err.Error(), "frame header") {
		t.Fatalf("Open error = %v, want oversized-frame header rejection", err)
	}
	if out.Len() != 0 {
		t.Fatalf("destination changed after oversized-frame rejection: %q", out.String())
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
	key := testKey()
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
	key := testKey()
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

func TestSealSourceFailureLeavesDestinationUntouched(t *testing.T) {
	metadata := testMetadata()
	resolver := testResolver{metadata.Reference(): testKey()}
	var sealOut bytes.Buffer
	sealOut.WriteString("unchanged")
	if _, err := Seal(&sealOut, &interruptedReader{}, resolver, metadata); err == nil {
		t.Fatal("Seal accepted interrupted source")
	}
	if sealOut.String() != "unchanged" {
		t.Fatalf("Seal wrote residue after source interruption: %q", sealOut.String())
	}
}

func TestOpenAuthenticationAndTruncationFailuresLeaveDestinationUntouched(t *testing.T) {
	metadata := testMetadata()
	resolver := testResolver{metadata.Reference(): testKey()}
	var sealed bytes.Buffer
	if _, err := Seal(&sealed, strings.NewReader("secret payload"), resolver, metadata); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		data []byte
	}{
		{name: "authentication", data: tamperLastByte(sealed.Bytes())},
		{name: "truncation", data: append([]byte(nil), sealed.Bytes()[:len(sealed.Bytes())-1]...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			out.WriteString("unchanged")
			if err := Open(&out, bytes.NewReader(tc.data), resolver, metadata); err == nil {
				t.Fatal("Open accepted invalid artifact")
			}
			if out.String() != "unchanged" {
				t.Fatalf("Open wrote residue after %s failure: %q", tc.name, out.String())
			}
		})
	}
}

func tamperLastByte(data []byte) []byte {
	tampered := append([]byte(nil), data...)
	tampered[len(tampered)-1] ^= 0x01
	return tampered
}

type trackingReader struct {
	reader  io.Reader
	maxRead int
}

func (r *trackingReader) Read(p []byte) (int, error) {
	if len(p) > r.maxRead {
		r.maxRead = len(p)
	}
	return r.reader.Read(p)
}

type trackingWriter struct {
	bytes.Buffer
	maxWrite int
}

func (w *trackingWriter) Write(p []byte) (int, error) {
	if len(p) > w.maxWrite {
		w.maxWrite = len(p)
	}
	return w.Buffer.Write(p)
}

func artifactFrameCount(data []byte) int {
	headerLen := binary.BigEndian.Uint32(data[len(magic) : len(magic)+4])
	offset := len(magic) + 4 + int(headerLen)
	count := 0
	for offset+17 <= len(data) {
		kind := data[offset]
		cipherLen := binary.BigEndian.Uint32(data[offset+13 : offset+17])
		offset += 17 + int(cipherLen)
		count++
		if kind == 2 {
			break
		}
	}
	return count
}

func TestLargeArtifactsUseBoundedReadAndWriteChunks(t *testing.T) {
	metadata := testMetadata()
	resolver := testResolver{metadata.Reference(): testKey()}
	plaintext := bytes.Repeat([]byte("large-restore-data-"), 4*chunkSize)
	sealSource := &trackingReader{reader: bytes.NewReader(plaintext)}
	var sealed trackingWriter
	if _, err := Seal(&sealed, sealSource, resolver, metadata); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if artifactFrameCount(sealed.Bytes()) < 5 {
		t.Fatalf("sealed artifact frame count = %d, want multiple payload frames and footer", artifactFrameCount(sealed.Bytes()))
	}
	if sealSource.maxRead > chunkSize {
		t.Fatalf("Seal source read size = %d, want <= %d", sealSource.maxRead, chunkSize)
	}
	if sealed.maxWrite > chunkSize {
		t.Fatalf("Seal destination write size = %d, want <= %d", sealed.maxWrite, chunkSize)
	}

	openSource := &trackingReader{reader: bytes.NewReader(sealed.Bytes())}
	var opened trackingWriter
	if err := Open(&opened, openSource, resolver, metadata); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(opened.Bytes(), plaintext) {
		t.Fatal("opened plaintext differs from large sealed input")
	}
	if openSource.maxRead > chunkSize {
		t.Fatalf("Open source read size = %d, want <= %d", openSource.maxRead, chunkSize)
	}
	if opened.maxWrite > chunkSize {
		t.Fatalf("Open destination write size = %d, want <= %d", opened.maxWrite, chunkSize)
	}
}

func TestSpoolsArePrivateAndRemovedOnSuccessAndFailure(t *testing.T) {
	tempDir := t.TempDir()
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, tempDir)
	}
	if actual := os.TempDir(); actual != tempDir {
		t.Fatalf("isolated temporary directory = %q, want %q", actual, tempDir)
	}
	before := artifactSpoolEntries(t, tempDir)
	metadata := testMetadata()
	resolver := testResolver{metadata.Reference(): testKey()}
	var sealed bytes.Buffer
	if _, err := Seal(&sealed, strings.NewReader("payload"), resolver, metadata); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	var opened bytes.Buffer
	if err := Open(&opened, bytes.NewReader(sealed.Bytes()), resolver, metadata); err != nil {
		t.Fatalf("Open: %v", err)
	}
	var untouched bytes.Buffer
	untouched.WriteString("unchanged")
	if _, err := Seal(&untouched, &interruptedReader{}, resolver, metadata); err == nil {
		t.Fatal("Seal accepted interrupted source")
	}
	if err := Open(&untouched, bytes.NewReader(tamperLastByte(sealed.Bytes())), resolver, metadata); err == nil {
		t.Fatal("Open accepted tampered artifact")
	}
	if _, err := Seal(&errorWriter{err: errors.New("destination failed")}, strings.NewReader("payload"), resolver, metadata); err == nil {
		t.Fatal("Seal accepted destination failure")
	}
	if err := Open(&errorWriter{err: errors.New("destination failed")}, bytes.NewReader(sealed.Bytes()), resolver, metadata); err == nil {
		t.Fatal("Open accepted destination failure")
	}
	after := artifactSpoolEntries(t, tempDir)
	for name := range after {
		if _, ok := before[name]; !ok {
			t.Fatalf("temporary spool file remains: %q", name)
		}
	}
}

func artifactSpoolEntries(t *testing.T, tempDir string) map[string]struct{} {
	t.Helper()
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	prefix := strings.TrimSuffix(spoolPattern, "*")
	files := make(map[string]struct{})
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			files[entry.Name()] = struct{}{}
		}
	}
	return files
}

type shortWriter struct {
	bytes.Buffer
}

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n := len(p) - 1
	_, _ = w.Buffer.Write(p[:n])
	return n, nil
}

type errorWriter struct {
	err error
}

func (w *errorWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestDestinationShortAndErrorWritesAreReported(t *testing.T) {
	metadata := testMetadata()
	resolver := testResolver{metadata.Reference(): testKey()}
	var sealed bytes.Buffer
	if _, err := Seal(&sealed, strings.NewReader(strings.Repeat("payload", chunkSize)), resolver, metadata); err != nil {
		t.Fatal(err)
	}
	operations := []struct {
		name string
		run  func(io.Writer) error
	}{
		{
			name: "seal",
			run: func(dst io.Writer) error {
				_, err := Seal(dst, strings.NewReader("payload"), resolver, metadata)
				return err
			},
		},
		{
			name: "open",
			run: func(dst io.Writer) error {
				return Open(dst, bytes.NewReader(sealed.Bytes()), resolver, metadata)
			},
		},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			t.Run("short write", func(t *testing.T) {
				err := operation.run(&shortWriter{})
				if !errors.Is(err, io.ErrShortWrite) {
					t.Fatalf("error = %v, want io.ErrShortWrite", err)
				}
			})
			t.Run("destination error", func(t *testing.T) {
				want := errors.New("destination sink failed")
				err := operation.run(&errorWriter{err: want})
				if !errors.Is(err, want) {
					t.Fatalf("error = %v, want destination error", err)
				}
			})
		})
	}
}

func TestResolverErrorsDoNotExposeKeyOrPathValues(t *testing.T) {
	metadata := testMetadata()
	secret := "0123456789abcdef0123456789abcdef"
	path := `C:\private\artifact.bin`
	resolver := ResolverFunc(func(KeyReference) ([]byte, error) {
		return nil, fmt.Errorf("key=%s path=%s", secret, path)
	})
	_, err := Seal(io.Discard, strings.NewReader("payload"), resolver, metadata)
	if err == nil {
		t.Fatal("Seal succeeded with failing resolver")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), path) {
		t.Fatalf("resolver error exposed sensitive values: %v", err)
	}
}

type frameBoundarySentinelReader struct {
	reader io.Reader
	offset int
	limit  int
	err    error
}

func (r *frameBoundarySentinelReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.offset += n
	if r.offset >= r.limit && r.err != nil {
		return n, r.err
	}
	return n, err
}

func TestOpenPropagatesBoundarySentinelErrorWithoutSwallowing(t *testing.T) {
	metadata := testMetadata()
	resolver := testResolver{metadata.Reference(): testKey()}
	var sealed bytes.Buffer
	if _, err := Seal(&sealed, strings.NewReader("payload"), resolver, metadata); err != nil {
		t.Fatal(err)
	}
	headerLen := binary.BigEndian.Uint32(sealed.Bytes()[len(magic) : len(magic)+4])
	framePayloadOffset := len(magic) + 4 + int(headerLen) + 1 + 8 + 4 + 4
	sentinel := errors.New("boundary sentinel read failure")
	src := &frameBoundarySentinelReader{
		reader: bytes.NewReader(sealed.Bytes()),
		limit:  framePayloadOffset,
		err:    sentinel,
	}
	var out bytes.Buffer
	err := Open(&out, src, resolver, metadata)
	if err == nil || !errors.Is(err, sentinel) {
		t.Fatalf("Open error = %v, want sentinel error %v", err, sentinel)
	}
	if out.Len() != 0 {
		t.Fatalf("Open wrote output on boundary error: %q", out.String())
	}
}

func TestResolverBuffersAreZeroedAndValidatedEarly(t *testing.T) {
	metadata := testMetadata()
	originalKey := append([]byte(nil), testKey()...)
	resolverKey := append([]byte(nil), originalKey...)
	resolver := ResolverFunc(func(KeyReference) ([]byte, error) {
		return resolverKey, nil
	})
	var sealed bytes.Buffer
	if _, err := Seal(&sealed, strings.NewReader("payload"), resolver, metadata); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	allZero := true
	for _, b := range resolverKey {
		if b != 0 {
			allZero = false
			break
		}
	}
	if !allZero {
		t.Fatal("Seal did not zero original resolver key buffer")
	}

	invalidResolver := ResolverFunc(func(KeyReference) ([]byte, error) {
		return []byte("short-key"), nil
	})
	if _, err := Seal(io.Discard, strings.NewReader("payload"), invalidResolver, metadata); err == nil {
		t.Fatal("Seal accepted invalid key length")
	}
}

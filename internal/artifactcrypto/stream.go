package artifactcrypto

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
)

var magic = []byte("BDART1\x00")

const chunkSize = 64 * 1024

type KeyReference struct {
	ID      string `json:"id"`
	Version uint64 `json:"version"`
}

// KeyRef is kept as a descriptive alias for callers that prefer the shorter name.
type KeyRef = KeyReference
type Reference = KeyReference

type Resolver interface {
	Resolve(KeyReference) ([]byte, error)
}

type KeyResolver interface {
	ResolveKey(KeyReference) ([]byte, error)
}

type ContextKeyResolver interface {
	Resolve(context.Context, KeyReference) ([]byte, error)
}

type ResolverFunc func(KeyReference) ([]byte, error)

func (f ResolverFunc) Resolve(reference KeyReference) ([]byte, error) {
	return f(reference)
}

type Metadata struct {
	RestoreSetID string `json:"restore_set_id"`
	Component    string `json:"component"`
	KeyRef       string `json:"key_ref"`
	KeyVersion   uint64 `json:"key_version"`
}

type SealResult struct {
	PlaintextDigest  string
	CiphertextDigest string
}

type header struct {
	Version   int      `json:"version"`
	Metadata  Metadata `json:"metadata"`
	Nonce     []byte   `json:"nonce"`
	ChunkSize int      `json:"chunk_size"`
}

func (m Metadata) Reference() KeyReference {
	version := m.KeyVersion
	if version == 0 {
		version = 1
	}
	return KeyReference{ID: m.KeyRef, Version: version}
}

// Seal resolves the requested key in memory and commits the complete artifact
// to dst only after the plaintext source and authenticated footer are ready.
// The source parameter accepts a Resolver (preferred) or a raw key for
// compatibility with older callers; raw keys are never encoded in the artifact.
func Seal(dst io.Writer, src io.Reader, source any, metadata Metadata) (SealResult, error) {
	metadata, key, err := resolveSealKey(source, metadata)
	if err != nil {
		return SealResult{}, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return SealResult{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return SealResult{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return SealResult{}, fmt.Errorf("generate artifact nonce: %w", err)
	}
	h := header{Version: 1, Metadata: metadata, Nonce: nonce, ChunkSize: chunkSize}
	headerBytes, err := json.Marshal(h)
	if err != nil {
		return SealResult{}, err
	}
	var artifact bytes.Buffer
	cipherHash := sha256.New()
	write := func(data []byte) error {
		if _, err := artifact.Write(data); err != nil {
			return err
		}
		_, _ = cipherHash.Write(data)
		return nil
	}
	if err := write(magic); err != nil {
		return SealResult{}, err
	}
	if err := writeUint32(write, uint32(len(headerBytes))); err != nil {
		return SealResult{}, err
	}
	if err := write(headerBytes); err != nil {
		return SealResult{}, err
	}

	plainHash := sha256.New()
	buf := make([]byte, chunkSize)
	var counter uint64
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			_, _ = plainHash.Write(buf[:n])
			if err := writeFrame(write, gcm, nonce, headerBytes, 1, counter, buf[:n]); err != nil {
				return SealResult{}, err
			}
			counter++
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return SealResult{}, fmt.Errorf("read artifact plaintext: %w", readErr)
		}
		if n == 0 {
			return SealResult{}, fmt.Errorf("artifact source returned no data without EOF")
		}
	}
	plainDigest := "sha256:" + hex.EncodeToString(plainHash.Sum(nil))
	if err := writeFrame(write, gcm, nonce, headerBytes, 2, counter, []byte(plainDigest)); err != nil {
		return SealResult{}, err
	}
	if err := writeAll(dst, artifact.Bytes()); err != nil {
		return SealResult{}, fmt.Errorf("write sealed artifact: %w", err)
	}
	return SealResult{PlaintextDigest: plainDigest, CiphertextDigest: "sha256:" + hex.EncodeToString(cipherHash.Sum(nil))}, nil
}

// Open authenticates every frame and the final plaintext digest before it
// commits any plaintext to dst. Resolver failures, wrong references, key
// mismatches, truncation, and footer tampering therefore leave dst untouched.
func Open(dst io.Writer, src io.Reader, source any, expected Metadata) error {
	gotMagic := make([]byte, len(magic))
	if _, err := io.ReadFull(src, gotMagic); err != nil {
		return fmt.Errorf("read artifact header: %w", err)
	}
	if !bytes.Equal(gotMagic, magic) {
		return fmt.Errorf("artifact magic mismatch")
	}
	var headerLen uint32
	if err := binary.Read(src, binary.BigEndian, &headerLen); err != nil || headerLen == 0 || headerLen > 64*1024 {
		return fmt.Errorf("artifact header length invalid")
	}
	headerBytes := make([]byte, headerLen)
	if _, err := io.ReadFull(src, headerBytes); err != nil {
		return fmt.Errorf("read artifact metadata: %w", err)
	}
	var h header
	if err := json.Unmarshal(headerBytes, &h); err != nil || h.Version != 1 || h.ChunkSize != chunkSize {
		return fmt.Errorf("artifact metadata invalid")
	}
	if err := compareMetadata(h.Metadata, expected); err != nil {
		return err
	}
	key, err := resolveOpenKey(source, h.Metadata)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	if len(h.Nonce) != gcm.NonceSize() {
		return fmt.Errorf("artifact metadata invalid")
	}
	var plaintext bytes.Buffer
	plainHash := sha256.New()
	var expectedCounter uint64
	for {
		kind, err := readByte(src)
		if err != nil {
			return fmt.Errorf("artifact truncated before footer: %w", err)
		}
		counter, plainLen, cipherLen, err := readFrameHeader(src)
		if err != nil {
			return err
		}
		if kind != 1 && kind != 2 {
			return fmt.Errorf("artifact frame header invalid: unknown kind %d", kind)
		}
		if counter != expectedCounter || plainLen > uint32(h.ChunkSize) || uint64(cipherLen) != uint64(plainLen)+uint64(gcm.Overhead()) {
			return fmt.Errorf("artifact frame header invalid")
		}
		ciphertext := make([]byte, cipherLen)
		if _, err := io.ReadFull(src, ciphertext); err != nil {
			return fmt.Errorf("artifact truncated frame: %w", err)
		}
		decrypted, err := gcm.Open(nil, deriveNonce(h.Nonce, counter), ciphertext, frameAAD(headerBytes, kind, counter, plainLen))
		if err != nil {
			return fmt.Errorf("artifact authentication failed: %w", err)
		}
		if kind == 1 {
			if _, err := plaintext.Write(decrypted); err != nil {
				return err
			}
			_, _ = plainHash.Write(decrypted)
			expectedCounter++
			continue
		}
		if counter != expectedCounter {
			return fmt.Errorf("artifact footer invalid")
		}
		wantDigest := string(decrypted)
		gotDigest := "sha256:" + hex.EncodeToString(plainHash.Sum(nil))
		if wantDigest != gotDigest {
			return fmt.Errorf("artifact plaintext digest mismatch")
		}
		var trailing [1]byte
		if n, trailingErr := src.Read(trailing[:]); n != 0 || trailingErr != io.EOF {
			return fmt.Errorf("artifact has trailing data")
		}
		if err := writeAll(dst, plaintext.Bytes()); err != nil {
			return fmt.Errorf("write opened artifact: %w", err)
		}
		return nil
	}
}

func SealWithResolver(dst io.Writer, src io.Reader, resolver Resolver, metadata Metadata) (SealResult, error) {
	return Seal(dst, src, resolver, metadata)
}

func OpenWithResolver(dst io.Writer, src io.Reader, resolver Resolver, expected Metadata) error {
	return Open(dst, src, resolver, expected)
}

func resolveSealKey(source any, metadata Metadata) (Metadata, []byte, error) {
	if _, raw := source.([]byte); raw && metadata.KeyVersion == 0 {
		metadata.KeyVersion = 1
	}
	if err := validateMetadata(metadata); err != nil {
		return Metadata{}, nil, err
	}
	key, err := resolveKey(source, metadata.Reference())
	if err != nil {
		return Metadata{}, nil, fmt.Errorf("resolve artifact key %s@%d: %w", metadata.KeyRef, metadata.KeyVersion, err)
	}
	if err := validateKey(key); err != nil {
		return Metadata{}, nil, err
	}
	return metadata, key, nil
}

func resolveOpenKey(source any, metadata Metadata) ([]byte, error) {
	if _, raw := source.([]byte); raw && metadata.KeyVersion == 0 {
		metadata.KeyVersion = 1
	}
	if err := validateMetadata(metadata); err != nil {
		return nil, err
	}
	key, err := resolveKey(source, metadata.Reference())
	if err != nil {
		return nil, fmt.Errorf("resolve artifact key %s@%d: %w", metadata.KeyRef, metadata.KeyVersion, err)
	}
	if err := validateKey(key); err != nil {
		return nil, err
	}
	return key, nil
}

func resolveKey(source any, reference KeyReference) ([]byte, error) {
	switch resolver := source.(type) {
	case []byte:
		return append([]byte(nil), resolver...), nil
	case Resolver:
		return resolver.Resolve(reference)
	case KeyResolver:
		return resolver.ResolveKey(reference)
	case ContextKeyResolver:
		return resolver.Resolve(context.Background(), reference)
	case func(KeyReference) ([]byte, error):
		return resolver(reference)
	default:
		return nil, fmt.Errorf("unsupported artifact key source %T", source)
	}
}

func writeAll(dst io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := dst.Write(data)
		if n > 0 {
			data = data[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func validateKey(key []byte) error {
	if len(key) != 16 && len(key) != 24 && len(key) != 32 {
		return fmt.Errorf("artifact key must be 16, 24, or 32 bytes")
	}
	return nil
}
func validateMetadata(metadata Metadata) error {
	if metadata.RestoreSetID == "" || metadata.Component == "" || metadata.KeyRef == "" {
		return fmt.Errorf("artifact metadata requires restore_set_id, component, and key_ref")
	}
	if metadata.KeyVersion == 0 {
		return fmt.Errorf("artifact metadata requires key_version")
	}
	return nil
}

func compareMetadata(got, expected Metadata) error {
	if expected.RestoreSetID != "" && got.RestoreSetID != expected.RestoreSetID ||
		expected.Component != "" && got.Component != expected.Component ||
		expected.KeyRef != "" && got.KeyRef != expected.KeyRef ||
		expected.KeyVersion != 0 && got.KeyVersion != expected.KeyVersion {
		return fmt.Errorf("artifact metadata mismatch")
	}
	return nil
}

func deriveNonce(base []byte, counter uint64) []byte {
	nonce := append([]byte(nil), base...)
	for i := range 8 {
		nonce[len(nonce)-1-i] ^= byte(counter >> (8 * i))
	}
	return nonce
}

func frameAAD(header []byte, kind byte, counter uint64, plainLen uint32) []byte {
	var frame [13]byte
	frame[0] = kind
	binary.BigEndian.PutUint64(frame[1:9], counter)
	binary.BigEndian.PutUint32(frame[9:13], plainLen)
	return append(append([]byte(nil), header...), frame[:]...)
}

func writeUint32(write func([]byte) error, value uint32) error {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], value)
	return write(buf[:])
}

func writeFrame(write func([]byte) error, gcm cipher.AEAD, nonce, header []byte, kind byte, counter uint64, plaintext []byte) error {
	ciphertext := gcm.Seal(nil, deriveNonce(nonce, counter), plaintext, frameAAD(header, kind, counter, uint32(len(plaintext))))
	if err := write([]byte{kind}); err != nil {
		return err
	}
	var frame [12]byte
	binary.BigEndian.PutUint64(frame[0:8], counter)
	binary.BigEndian.PutUint32(frame[8:12], uint32(len(plaintext)))
	if err := write(frame[:]); err != nil {
		return err
	}
	if err := writeUint32(write, uint32(len(ciphertext))); err != nil {
		return err
	}
	return write(ciphertext)
}

func readByte(src io.Reader) (byte, error) {
	var one [1]byte
	_, err := io.ReadFull(src, one[:])
	return one[0], err
}

func readFrameHeader(src io.Reader) (uint64, uint32, uint32, error) {
	var frame [12]byte
	if _, err := io.ReadFull(src, frame[:]); err != nil {
		return 0, 0, 0, fmt.Errorf("read artifact frame header: %w", err)
	}
	var cipherLen uint32
	if err := binary.Read(src, binary.BigEndian, &cipherLen); err != nil {
		return 0, 0, 0, fmt.Errorf("read artifact frame length: %w", err)
	}
	return binary.BigEndian.Uint64(frame[0:8]), binary.BigEndian.Uint32(frame[8:12]), cipherLen, nil
}

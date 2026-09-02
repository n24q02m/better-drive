package artifactcrypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

var magic = []byte("BDART1\x00")

const (
	chunkSize    = 64 * 1024
	spoolPattern = "better-drive-artifact-*"
)

type KeyReference struct {
	ID      string `json:"id"`
	Version uint64 `json:"version"`
}

type Resolver interface {
	Resolve(KeyReference) ([]byte, error)
}

type ResolverFunc func(KeyReference) ([]byte, error)

func (f ResolverFunc) Resolve(reference KeyReference) ([]byte, error) {
	if f == nil {
		return nil, errors.New("artifact key resolver is required")
	}
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

// Seal resolves the requested key and commits the complete artifact to dst
// only after the plaintext source and authenticated footer are ready.
func Seal(dst io.Writer, src io.Reader, resolver Resolver, metadata Metadata) (result SealResult, err error) {
	key, err := resolveSealKey(resolver, metadata)
	if err != nil {
		return result, err
	}
	defer zeroBytes(key)

	block, err := aes.NewCipher(key)
	if err != nil {
		return result, wrapError("create artifact cipher", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return result, wrapError("create artifact authenticator", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return result, wrapError("generate artifact nonce", err)
	}
	defer zeroBytes(nonce)

	h := header{Version: 1, Metadata: metadata, Nonce: nonce, ChunkSize: chunkSize}
	headerBytes, err := json.Marshal(h)
	if err != nil {
		return result, wrapError("marshal artifact metadata", err)
	}
	if len(headerBytes) == 0 || len(headerBytes) > chunkSize {
		return result, errors.New("artifact metadata invalid")
	}

	spool, err := createSpool()
	if err != nil {
		return result, err
	}
	defer func() {
		cleanupErr := cleanupSpool(spool)
		if cleanupErr != nil {
			result = SealResult{}
			err = errors.Join(err, cleanupErr)
		}
	}()

	cipherHash := sha256.New()
	write := func(data []byte) error {
		if err := writeAll(spool, data); err != nil {
			return wrapError("write artifact spool", err)
		}
		_, _ = cipherHash.Write(data)
		return nil
	}
	if err := write(magic); err != nil {
		return result, err
	}
	if err := writeUint32(write, uint32(len(headerBytes))); err != nil {
		return result, err
	}
	if err := write(headerBytes); err != nil {
		return result, err
	}

	plainHash := sha256.New()
	plainBuffer := make([]byte, chunkSize)
	defer zeroBytes(plainBuffer)
	cipherBuffer := make([]byte, 0, chunkSize+gcm.Overhead())
	defer func() {
		zeroBytes(cipherBuffer)
	}()

	var counter uint64
	for {
		n, readErr := src.Read(plainBuffer)
		if n < 0 || n > len(plainBuffer) {
			return result, errors.New("artifact source returned invalid read length")
		}
		if n > 0 {
			_, _ = plainHash.Write(plainBuffer[:n])
			cipherBuffer, err = writeFrameBuffered(write, gcm, nonce, headerBytes, 1, counter, plainBuffer[:n], cipherBuffer)
			if err != nil {
				return result, err
			}
			counter++
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return result, wrapError("read artifact plaintext", readErr)
		}
		if n == 0 {
			return result, errors.New("artifact source returned no data without EOF")
		}
	}

	plainDigest := "sha256:" + hex.EncodeToString(plainHash.Sum(nil))
	cipherBuffer, err = writeFrameBuffered(write, gcm, nonce, headerBytes, 2, counter, []byte(plainDigest), cipherBuffer)
	if err != nil {
		return result, err
	}
	if err := spool.Sync(); err != nil {
		return result, wrapError("sync artifact spool", err)
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return result, wrapError("rewind artifact spool", err)
	}
	if err := copySpool(dst, spool, plainBuffer); err != nil {
		return result, wrapError("write sealed artifact", err)
	}
	return SealResult{
		PlaintextDigest:  plainDigest,
		CiphertextDigest: "sha256:" + hex.EncodeToString(cipherHash.Sum(nil)),
	}, nil
}

// Open authenticates every frame and the final plaintext digest before it
// commits any plaintext to dst. Resolver failures, wrong references, key
// mismatches, truncation, and footer tampering therefore leave dst untouched.
func Open(dst io.Writer, src io.Reader, resolver Resolver, expected Metadata) (err error) {
	gotMagic := make([]byte, len(magic))
	if _, readErr := io.ReadFull(src, gotMagic); readErr != nil {
		return wrapError("read artifact header", readErr)
	}
	if !bytes.Equal(gotMagic, magic) {
		return errors.New("artifact magic mismatch")
	}

	var headerLen uint32
	if readErr := binary.Read(src, binary.BigEndian, &headerLen); readErr != nil || headerLen == 0 || headerLen > chunkSize {
		return errors.New("artifact header length invalid")
	}
	headerBytes := make([]byte, headerLen)
	if _, readErr := io.ReadFull(src, headerBytes); readErr != nil {
		return wrapError("read artifact metadata", readErr)
	}
	var h header
	if unmarshalErr := json.Unmarshal(headerBytes, &h); unmarshalErr != nil || h.Version != 1 || h.ChunkSize != chunkSize {
		return errors.New("artifact metadata invalid")
	}
	if err := compareMetadata(h.Metadata, expected); err != nil {
		return err
	}
	key, err := resolveOpenKey(resolver, h.Metadata)
	if err != nil {
		return err
	}
	defer zeroBytes(key)

	block, err := aes.NewCipher(key)
	if err != nil {
		return wrapError("create artifact cipher", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return wrapError("create artifact authenticator", err)
	}
	if len(h.Nonce) != gcm.NonceSize() {
		return errors.New("artifact metadata invalid")
	}

	spool, err := createSpool()
	if err != nil {
		return err
	}
	defer func() {
		cleanupErr := cleanupSpool(spool)
		if cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	}()
	plainBuffer := make([]byte, chunkSize)
	defer zeroBytes(plainBuffer)
	cipherBuffer := make([]byte, chunkSize+gcm.Overhead())
	defer zeroBytes(cipherBuffer)
	plainHash := sha256.New()
	var expectedCounter uint64
	for {
		kind, readErr := readByte(src)
		if readErr != nil {
			return wrapError("artifact truncated before footer", readErr)
		}
		counter, plainLen, cipherLen, readErr := readFrameHeader(src)
		if readErr != nil {
			return readErr
		}
		if kind != 1 && kind != 2 {
			return errors.New("artifact frame header invalid")
		}
		if counter != expectedCounter || plainLen > uint32(h.ChunkSize) || uint64(cipherLen) != uint64(plainLen)+uint64(gcm.Overhead()) {
			return errors.New("artifact frame header invalid")
		}
		if _, readErr := readBoundedFull(src, cipherBuffer[:int(cipherLen)]); readErr != nil {
			return wrapError("artifact truncated frame", readErr)
		}
		decrypted, authErr := gcm.Open(plainBuffer[:0], deriveNonce(h.Nonce, counter), cipherBuffer[:int(cipherLen)], frameAAD(headerBytes, kind, counter, plainLen))
		if authErr != nil {
			return wrapError("artifact authentication failed", authErr)
		}
		if uint32(len(decrypted)) != plainLen {
			return errors.New("artifact frame plaintext length invalid")
		}
		if kind == 1 {
			if writeErr := writeAll(spool, decrypted); writeErr != nil {
				return wrapError("write artifact spool", writeErr)
			}
			_, _ = plainHash.Write(decrypted)
			expectedCounter++
			continue
		}
		if counter != expectedCounter {
			return errors.New("artifact footer invalid")
		}
		wantDigest := string(decrypted)
		gotDigest := "sha256:" + hex.EncodeToString(plainHash.Sum(nil))
		if wantDigest != gotDigest {
			return errors.New("artifact plaintext digest mismatch")
		}
		var trailing [1]byte
		if n, trailingErr := src.Read(trailing[:]); n != 0 || trailingErr != io.EOF {
			return errors.New("artifact has trailing data")
		}
		if err := spool.Sync(); err != nil {
			return wrapError("sync artifact spool", err)
		}
		if _, err := spool.Seek(0, io.SeekStart); err != nil {
			return wrapError("rewind artifact spool", err)
		}
		if err := copySpool(dst, spool, plainBuffer); err != nil {
			return wrapError("write opened artifact", err)
		}
		return nil
	}
}

func resolveSealKey(resolver Resolver, metadata Metadata) ([]byte, error) {
	if err := validateMetadata(metadata); err != nil {
		return nil, err
	}
	return resolveKey(resolver, metadata.Reference())
}

func resolveOpenKey(resolver Resolver, metadata Metadata) ([]byte, error) {
	if err := validateMetadata(metadata); err != nil {
		return nil, err
	}
	return resolveKey(resolver, metadata.Reference())
}

func resolveKey(resolver Resolver, reference KeyReference) ([]byte, error) {
	if resolver == nil {
		return nil, errors.New("artifact key resolver is required")
	}
	resolved, err := resolver.Resolve(reference)
	if err != nil {
		return nil, wrapError("resolve artifact key", err)
	}
	if err := validateKey(resolved); err != nil {
		zeroBytes(resolved)
		return nil, err
	}
	key := append([]byte(nil), resolved...)
	zeroBytes(resolved)
	return key, nil
}

func createSpool() (*os.File, error) {
	spool, err := createSecureSpool()
	if err != nil {
		return nil, wrapError("create artifact spool", err)
	}
	return spool, nil
}

func cleanupSpool(spool *os.File) error {
	if spool == nil {
		return nil
	}
	return cleanupSecureSpool(spool)
}

func copySpool(dst io.Writer, spool *os.File, buffer []byte) error {
	reader := readerOnly{reader: spool}
	writer := writerOnly{writer: dst}
	_, err := io.CopyBuffer(writer, reader, buffer)
	return err
}

func readBoundedFull(src io.Reader, data []byte) (int, error) {
	total := 0
	for total < len(data) {
		readLen := len(data) - total
		if readLen > chunkSize {
			readLen = chunkSize
		}
		n, err := src.Read(data[total : total+readLen])
		if n < 0 || n > readLen {
			return total, errors.New("artifact reader returned invalid length")
		}
		total += n
		if err != nil {
			if err == io.EOF && total == len(data) {
				return total, nil
			}
			return total, err
		}
		if n == 0 {
			return total, io.ErrUnexpectedEOF
		}
	}
	return total, nil
}

type readerOnly struct {
	reader io.Reader
}

func (r readerOnly) Read(p []byte) (int, error) {
	return r.reader.Read(p)
}

type writerOnly struct {
	writer io.Writer
}

func (w writerOnly) Write(p []byte) (int, error) {
	return w.writer.Write(p)
}

func writeAll(dst io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := dst.Write(data)
		if n < 0 || n > len(data) {
			return io.ErrShortWrite
		}
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
		return errors.New("artifact key must be 16, 24, or 32 bytes")
	}
	return nil
}

func validateMetadata(metadata Metadata) error {
	if metadata.RestoreSetID == "" || metadata.Component == "" || metadata.KeyRef == "" {
		return errors.New("artifact metadata requires restore_set_id, component, and key_ref")
	}
	if metadata.KeyVersion == 0 {
		return errors.New("artifact metadata requires key_version")
	}
	return nil
}

func compareMetadata(got, expected Metadata) error {
	if expected.RestoreSetID != "" && got.RestoreSetID != expected.RestoreSetID ||
		expected.Component != "" && got.Component != expected.Component ||
		expected.KeyRef != "" && got.KeyRef != expected.KeyRef ||
		expected.KeyVersion != 0 && got.KeyVersion != expected.KeyVersion {
		return errors.New("artifact metadata mismatch")
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
	ciphertext, err := writeFrameBuffered(write, gcm, nonce, header, kind, counter, plaintext, nil)
	zeroBytes(ciphertext)
	return err
}

func writeFrameBuffered(write func([]byte) error, gcm cipher.AEAD, nonce, header []byte, kind byte, counter uint64, plaintext, ciphertext []byte) ([]byte, error) {
	if cap(ciphertext) < len(plaintext)+gcm.Overhead() {
		ciphertext = make([]byte, 0, len(plaintext)+gcm.Overhead())
	}
	ciphertext = gcm.Seal(ciphertext[:0], deriveNonce(nonce, counter), plaintext, frameAAD(header, kind, counter, uint32(len(plaintext))))
	if err := write([]byte{kind}); err != nil {
		return ciphertext, err
	}
	var frame [12]byte
	binary.BigEndian.PutUint64(frame[0:8], counter)
	binary.BigEndian.PutUint32(frame[8:12], uint32(len(plaintext)))
	if err := write(frame[:]); err != nil {
		return ciphertext, err
	}
	if err := writeUint32(write, uint32(len(ciphertext))); err != nil {
		return ciphertext, err
	}
	for remaining := ciphertext; len(remaining) > 0; {
		writeLen := len(remaining)
		if writeLen > chunkSize {
			writeLen = chunkSize
		}
		if err := write(remaining[:writeLen]); err != nil {
			return ciphertext, err
		}
		remaining = remaining[writeLen:]
	}
	return ciphertext, nil
}

func readByte(src io.Reader) (byte, error) {
	var one [1]byte
	_, err := io.ReadFull(src, one[:])
	return one[0], err
}

func readFrameHeader(src io.Reader) (uint64, uint32, uint32, error) {
	var frame [12]byte
	if _, err := io.ReadFull(src, frame[:]); err != nil {
		return 0, 0, 0, wrapError("read artifact frame header", err)
	}
	var cipherLen uint32
	if err := binary.Read(src, binary.BigEndian, &cipherLen); err != nil {
		return 0, 0, 0, wrapError("read artifact frame length", err)
	}
	return binary.BigEndian.Uint64(frame[0:8]), binary.BigEndian.Uint32(frame[8:12]), cipherLen, nil
}

func zeroBytes(data []byte) {
	full := data[:cap(data)]
	for i := range full {
		full[i] = 0
	}
}

type safeError struct {
	message string
	cause   error
}

func (e *safeError) Error() string {
	return e.message
}

func (e *safeError) Format(state fmt.State, verb rune) {
	switch verb {
	case 'q':
		_, _ = fmt.Fprintf(state, "%q", e.message)
	default:
		_, _ = io.WriteString(state, e.message)
	}
}

func (e *safeError) Unwrap() error {
	return e.cause
}

func wrapError(message string, cause error) error {
	if cause == nil {
		return errors.New(message)
	}
	return &safeError{message: message, cause: cause}
}

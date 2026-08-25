// Package runlog provides bounded, redacted run events and append-only log files.
package runlog

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	StreamStdout Stream = "stdout"
	StreamStderr Stream = "stderr"
	StreamSystem Stream = "system"

	DefaultMaxMessageBytes = 64 * 1024
	DefaultMaxLogBytes     = 4 * 1024 * 1024
	DefaultMaxLogAge       = 24 * time.Hour
	DefaultRetainFiles     = 3
)

type Stream string

func (s Stream) valid() bool {
	return s == StreamStdout || s == StreamStderr || s == StreamSystem
}

// Event is the stable wire record for one observed run event. Terminal is true
// for exactly one final record in a run.
type Event struct {
	RunID     string    `json:"run_id"`
	Sequence  uint64    `json:"sequence"`
	Timestamp time.Time `json:"timestamp"`
	Stream    Stream    `json:"stream"`
	Message   string    `json:"message"`
	Terminal  bool      `json:"terminal,omitempty"`
	Outcome   string    `json:"outcome,omitempty"`
}

var (
	ErrTerminalWritten = errors.New("runlog terminal event already written")
	ErrInvalidStream   = errors.New("runlog invalid stream")
	ErrInvalidOutcome  = errors.New("runlog invalid terminal outcome")
)

var (
	authorizationPattern = regexp.MustCompile(`(?i)(["']?\bauthorization["']?\s*[:=]\s*["']?(?:bearer\s+)?)[^\s,;"'}\]]+`)
	credentialPattern    = regexp.MustCompile(`(?i)(["']?\b(?:access[_-]?token|refresh[_-]?token|client[_-]?secret|api[_-]?key|password|passwd|secret|credential(?:[_-]?ref)?|token)["']?\s*[:=]\s*["']?)[^\s,;"'}\]]+`)
	bearerPattern        = regexp.MustCompile(`(?i)(\bbearer\s+)[^\s,;"'}\]]+`)
	jwtPattern           = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`)
)

// Redact removes credential-shaped values before data reaches output, state,
// or disk. It deliberately keeps field names so diagnostics remain useful.
func Redact(message string) string {
	message = authorizationPattern.ReplaceAllString(message, `${1}<redacted>`)
	message = bearerPattern.ReplaceAllString(message, `${1}<redacted>`)
	message = credentialPattern.ReplaceAllString(message, `${1}<redacted>`)
	return jwtPattern.ReplaceAllString(message, `<redacted>`)
}

// Sink serializes events in observed writer order. Message storage is bounded,
// and timestamps are forced strictly increasing even when the wall clock does
// not advance between writes.
type Sink struct {
	mu          sync.Mutex
	writer      io.Writer
	runID       string
	next        uint64
	lastTime    time.Time
	terminal    bool
	maxMessage  int
	writeRecord func(Event) error
}

func NewSink(runID string, writer io.Writer) *Sink {
	if strings.TrimSpace(runID) == "" {
		runID = NewRunID()
	}
	if writer == nil {
		writer = io.Discard
	}
	s := &Sink{writer: writer, runID: runID, maxMessage: DefaultMaxMessageBytes}
	s.writeRecord = func(event Event) error {
		enc := json.NewEncoder(writer)
		return enc.Encode(event)
	}
	return s
}

// SetMaxMessageBytes bounds each event message; values <= 0 restore the
// package default. It is intended for setup before concurrent writes.
func (s *Sink) SetMaxMessageBytes(limit int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = DefaultMaxMessageBytes
	}
	s.maxMessage = limit
}

func (s *Sink) nextTimestamp(now time.Time) time.Time {
	now = now.UTC()
	if now.IsZero() {
		now = time.Unix(0, 0).UTC()
	}
	if !s.lastTime.IsZero() && !now.After(s.lastTime) {
		now = s.lastTime.Add(time.Nanosecond)
	}
	s.lastTime = now
	return now
}

func boundedMessage(message string, limit int) string {
	message = Redact(message)
	if limit <= 0 {
		limit = DefaultMaxMessageBytes
	}
	if len(message) <= limit {
		return message
	}
	const suffix = "...[truncated]"
	if limit <= len(suffix) {
		return suffix[:limit]
	}
	return message[:limit-len(suffix)] + suffix
}

func (s *Sink) emit(stream Stream, message string, terminal bool, outcome string) (Event, error) {
	if !stream.valid() {
		return Event{}, ErrInvalidStream
	}
	if terminal {
		switch outcome {
		case "success", "error", "panic", "cancelled":
		default:
			return Event{}, ErrInvalidOutcome
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal {
		return Event{}, ErrTerminalWritten
	}
	s.next++
	event := Event{
		RunID: s.runID, Sequence: s.next, Timestamp: s.nextTimestamp(time.Now().UTC()),
		Stream: stream, Message: boundedMessage(message, s.maxMessage), Terminal: terminal, Outcome: outcome,
	}
	if err := s.writeRecord(event); err != nil {
		s.next--
		return Event{}, err
	}
	if terminal {
		s.terminal = true
	}
	return event, nil
}

func (s *Sink) Emit(stream Stream, message string) (Event, error) {
	return s.emit(stream, message, false, "")
}

func (s *Sink) Terminal(outcome, message string) (Event, error) {
	return s.emit(StreamSystem, message, true, outcome)
}

func (s *Sink) TerminalWritten() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminal
}

// StreamWriter adapts an observed stdout/stderr stream to a Sink. Each Write
// is one observed chunk, so interleaving between independent writers is kept at
// the seam where it was observed rather than reconstructed later.
type StreamWriter struct {
	sink   *Sink
	stream Stream
}

func (w *StreamWriter) Write(p []byte) (int, error) {
	if w == nil || w.sink == nil {
		return len(p), nil
	}
	if len(p) == 0 {
		return 0, nil
	}
	if _, err := w.sink.Emit(w.stream, string(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *Sink) Writer(stream Stream) io.Writer { return &StreamWriter{sink: s, stream: stream} }
func (s *Sink) Stdout() io.Writer              { return s.Writer(StreamStdout) }
func (s *Sink) Stderr() io.Writer              { return s.Writer(StreamStderr) }
func (s *Sink) System() io.Writer              { return s.Writer(StreamSystem) }

// Run executes fn and emits exactly one terminal event for success, error,
// panic, or cancellation. Panics are converted to errors after the terminal
// event is recorded so callers can keep the lifecycle deterministic.
func Run(ctx context.Context, sink *Sink, fn func() error) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if sink == nil {
		return runWithoutSink(ctx, fn)
	}
	defer func() {
		outcome := "success"
		message := "run completed"
		if recovered := recover(); recovered != nil {
			outcome = "panic"
			err = fmt.Errorf("run panic: %v", recovered)
			message = err.Error()
		} else if ctx.Err() != nil {
			outcome = "cancelled"
			err = ctx.Err()
			message = err.Error()
		} else if err != nil {
			outcome = "error"
			message = err.Error()
		}
		if _, terminalErr := sink.Terminal(outcome, message); terminalErr != nil && !errors.Is(terminalErr, ErrTerminalWritten) && err == nil {
			err = terminalErr
		}
	}()
	if ctx.Err() != nil {
		err = ctx.Err()
		return err
	}
	return fn()
}

func runWithoutSink(ctx context.Context, fn func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("run panic: %v", recovered)
		}
	}()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fn()
}

// RotationOptions controls size/age rotation. Retain is the number of rotated
// siblings (path.1, path.2, ...), not counting the active file.
type RotationOptions struct {
	MaxBytes int64
	MaxAge   time.Duration
	Retain   int
	Now      func() time.Time
	FileMode os.FileMode
}

func (o RotationOptions) normalized() RotationOptions {
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxLogBytes
	}
	if o.MaxAge <= 0 {
		o.MaxAge = DefaultMaxLogAge
	}
	if o.Retain < 0 {
		o.Retain = 0
	}
	if o.Retain == 0 {
		o.Retain = DefaultRetainFiles
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.FileMode == 0 {
		o.FileMode = 0o600
	}
	return o
}

type RotatingFile struct {
	mu     sync.Mutex
	path   string
	opts   RotationOptions
	file   *os.File
	size   int64
	opened time.Time
}

func OpenRotatingFile(path string, opts RotationOptions) (*RotatingFile, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("runlog path is required")
	}
	opts = opts.normalized()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create runlog directory: %w", err)
	}
	f := &RotatingFile{path: path, opts: opts}
	if err := f.openLocked(); err != nil {
		return nil, err
	}
	return f, nil
}

func (f *RotatingFile) now() time.Time { return f.opts.Now().UTC() }

func (f *RotatingFile) openLocked() error {
	file, err := os.OpenFile(f.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, f.opts.FileMode)
	if err != nil {
		return fmt.Errorf("open runlog: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("stat runlog: %w", err)
	}
	f.file = file
	f.size = info.Size()
	if f.size == 0 {
		f.opened = f.now()
	} else {
		f.opened = info.ModTime().UTC()
		if f.opened.IsZero() {
			f.opened = f.now()
		}
	}
	return nil
}

func (f *RotatingFile) closeLocked() error {
	if f.file == nil {
		return nil
	}
	err := f.file.Close()
	f.file = nil
	return err
}

func (f *RotatingFile) rotateLocked() error {
	if err := f.closeLocked(); err != nil {
		return err
	}
	for i := f.opts.Retain; i >= 1; i-- {
		old := fmt.Sprintf("%s.%d", f.path, i)
		if i == f.opts.Retain {
			_ = os.Remove(old)
			continue
		}
		if err := os.Rename(old, fmt.Sprintf("%s.%d", f.path, i+1)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("shift rotated runlog: %w", err)
		}
	}
	if err := os.Rename(f.path, f.path+".1"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("rotate runlog: %w", err)
	}
	return f.openLocked()
}

func (f *RotatingFile) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == nil {
		if err := f.openLocked(); err != nil {
			return 0, err
		}
	}
	now := f.now()
	aged := f.opts.MaxAge > 0 && f.size > 0 && !f.opened.IsZero() && now.Sub(f.opened) >= f.opts.MaxAge
	oversized := f.opts.MaxBytes > 0 && f.size > 0 && f.size+int64(len(p)) > f.opts.MaxBytes
	if aged || oversized {
		if err := f.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := f.file.Write(p)
	f.size += int64(n)
	return n, err
}

func (f *RotatingFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closeLocked()
}

// RotateNow forces a deterministic rotation and is primarily useful for
// lifecycle boundaries and tests.
func (f *RotatingFile) RotateNow() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.size == 0 {
		return nil
	}
	return f.rotateLocked()
}

// FileLog combines a Sink with a rotating JSONL writer.
type FileLog struct {
	Sink *Sink
	File *RotatingFile
}

func OpenFile(path, runID string, opts RotationOptions) (*FileLog, error) {
	file, err := OpenRotatingFile(path, opts)
	if err != nil {
		return nil, err
	}
	return &FileLog{Sink: NewSink(runID, file), File: file}, nil
}

func (l *FileLog) Close() error {
	if l == nil || l.File == nil {
		return nil
	}
	return l.File.Close()
}

var runCounter uint64

func NewRunID() string {
	counter := atomic.AddUint64(&runCounter, 1)
	now := time.Now().UTC().Format("20060102T150405.000000000Z")
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", now, counter)))
	return "run-" + now + "-" + hex.EncodeToString(sum[:4])
}

// JSONLWriter is a convenience for writing events to an arbitrary stream
// while retaining buffered flush behavior for ordinary files.
type JSONLWriter struct {
	mu sync.Mutex
	w  *bufio.Writer
}

func NewJSONLWriter(w io.Writer) *JSONLWriter { return &JSONLWriter{w: bufio.NewWriter(w)} }
func (w *JSONLWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}
func (w *JSONLWriter) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Flush()
}

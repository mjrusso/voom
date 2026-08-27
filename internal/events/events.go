// Package events provides Voom's best-effort file-backed event stream.
package events

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// SchemaVersion is the event and stream-header schema understood by this package.
	SchemaVersion = 1
	maxRecordSize = 8 * 1024
	maxLogSize    = 4 * 1024 * 1024
)

var (
	// ErrCursorUnavailable means an event-ID anchor is outside retained history.
	ErrCursorUnavailable = errors.New("event cursor is no longer available")
	// ErrHistoryGap means the reader missed at least one retained generation.
	ErrHistoryGap = errors.New("event history gap detected")
)

// Actor identifies the object that changed.
type Actor struct {
	ID         string            `json:"id"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// Source identifies the process that emitted an event.
type Source struct {
	PID     int    `json:"pid"`
	Command string `json:"command,omitempty"`
}

// Event is one change notification in the Voom event stream.
type Event struct {
	RecordType    string `json:"recordType"`
	SchemaVersion int    `json:"schemaVersion"`
	ID            string `json:"id"`
	Time          string `json:"time"`
	TimeNano      int64  `json:"timeNano"`
	Type          string `json:"type"`
	Action        string `json:"action"`
	Actor         Actor  `json:"actor"`
	Source        Source `json:"source"`
}

type header struct {
	RecordType         string  `json:"recordType"`
	SchemaVersion      int     `json:"schemaVersion"`
	StreamID           string  `json:"streamID"`
	Generation         uint64  `json:"generation"`
	PreviousEventID    string  `json:"previousEventID,omitempty"`
	PreviousGeneration *uint64 `json:"previousGeneration,omitempty"`
}

// Emitter appends events without allowing logging failures to fail callers.
type Emitter struct {
	path string
	lock string
	warn func(string)
	mu   sync.Mutex
}

// NewEmitter returns an emitter for the global event log under cacheDir.
func NewEmitter(cacheDir string, warn func(string)) *Emitter {
	return &Emitter{
		path: filepath.Join(cacheDir, "events.jsonl"),
		lock: filepath.Join(cacheDir, "events.lock"),
		warn: warn,
	}
}

// Emit appends ev or drops it after reporting an optional diagnostic.
func (e *Emitter) Emit(ev Event) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.emit(ev); err != nil && e.warn != nil {
		e.warn(err.Error())
	}
}

func (e *Emitter) emit(ev Event) error {
	now := time.Now().UTC()
	ev.RecordType = "event"
	ev.SchemaVersion = SchemaVersion
	if ev.ID == "" {
		id, err := randomID()
		if err != nil {
			return err
		}
		ev.ID = id
	}
	if ev.TimeNano == 0 {
		ev.TimeNano = now.UnixNano()
	}
	if ev.Time == "" {
		ev.Time = now.Format(time.RFC3339Nano)
	}
	if ev.Source.PID == 0 {
		ev.Source.PID = os.Getpid()
	}
	line, err := encodeLine(ev)
	if err != nil {
		return err
	}
	if len(line) > maxRecordSize {
		return fmt.Errorf("event record exceeds %d bytes", maxRecordSize)
	}
	unlock, err := lockWithTimeout(e.lock, 100*time.Millisecond)
	if err != nil {
		return err
	}
	defer unlock()
	if err := os.MkdirAll(filepath.Dir(e.path), 0o755); err != nil {
		return err
	}
	h, size, err := ensureLog(e.path)
	if err != nil {
		return err
	}
	if size+int64(len(line)) > maxLogSize {
		h, err = rotate(e.path, h)
		if err != nil {
			return err
		}
		_ = h
	}
	f, err := os.OpenFile(e.path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	n, writeErr := f.Write(line)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if n != len(line) {
		return io.ErrShortWrite
	}
	return closeErr
}

func ensureLog(path string) (header, int64, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && info.Size() == 0) {
		streamID, idErr := randomID()
		if idErr != nil {
			return header{}, 0, idErr
		}
		h := header{RecordType: "header", SchemaVersion: SchemaVersion, StreamID: streamID}
		line, _ := encodeLine(h)
		if err := os.WriteFile(path, line, 0o644); err != nil {
			return header{}, 0, err
		}
		return h, int64(len(line)), nil
	}
	if err != nil {
		return header{}, 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		return header{}, 0, err
	}
	defer func() { _ = f.Close() }()
	h, err := readHeader(bufio.NewReader(f))
	return h, info.Size(), err
}

func rotate(path string, old header) (header, error) {
	lastID, err := lastEventID(path)
	if err != nil {
		return header{}, err
	}
	if err := os.Rename(path, path+".1"); err != nil {
		return header{}, err
	}
	previous := old.Generation
	next := header{
		RecordType: "header", SchemaVersion: SchemaVersion, StreamID: old.StreamID,
		Generation: old.Generation + 1, PreviousGeneration: &previous, PreviousEventID: lastID,
	}
	line, _ := encodeLine(next)
	if err := os.WriteFile(path, line, 0o644); err != nil {
		return header{}, err
	}
	return next, nil
}

func lastEventID(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReader(f)
	if _, err := readHeader(r); err != nil {
		return "", err
	}
	last := ""
	pending := []byte{}
	for {
		ev, err := readEvent(r, &pending)
		if errors.Is(err, io.EOF) {
			if len(pending) != 0 {
				return "", errors.New("event log ends with an incomplete record")
			}
			return last, nil
		}
		if err != nil {
			return "", err
		}
		last = ev.ID
	}
}

func encodeLine(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// IsID reports whether value has the event ID encoding used by this schema.
func IsID(value string) bool {
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func lockWithTimeout(path string, timeout time.Duration) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() {
				_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = f.Close()
			return nil, err
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, errors.New("timed out acquiring event lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func lock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}

func readHeader(r *bufio.Reader) (header, error) {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return header{}, fmt.Errorf("read event header: %w", err)
	}
	var h header
	if err := json.Unmarshal(line, &h); err != nil {
		return header{}, fmt.Errorf("decode event header: %w", err)
	}
	if h.RecordType != "header" || h.StreamID == "" {
		return header{}, errors.New("invalid event log header")
	}
	if h.SchemaVersion != SchemaVersion {
		return header{}, fmt.Errorf("unsupported event schema version %d", h.SchemaVersion)
	}
	return h, nil
}

// Since selects the first historical event to consider.
type Since struct {
	ID   string
	Time *time.Time
}

// Request configures replay and live event streaming.
type Request struct {
	Since *Since
	Until *time.Time
}

// Reader replays and tails an event log.
type Reader struct {
	path string
	lock string
	poll time.Duration
}

// NewReader returns a reader for the global event log under cacheDir.
func NewReader(cacheDir string) *Reader {
	return &Reader{path: filepath.Join(cacheDir, "events.jsonl"), lock: filepath.Join(cacheDir, "events.lock"), poll: 200 * time.Millisecond}
}

type openLog struct {
	f       *os.File
	r       *bufio.Reader
	header  header
	pending []byte
	lastID  string
}

// Stream delivers matching historical events and then follows the active log.
func (r *Reader) Stream(ctx context.Context, req Request, deliver func(Event) error) error {
	logs, err := r.snapshot(req.Since != nil)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && (req.Since == nil || req.Since.ID == "") {
			return r.waitForLog(ctx, req, deliver)
		}
		if errors.Is(err, os.ErrNotExist) && req.Since != nil && req.Since.ID != "" {
			return ErrCursorUnavailable
		}
		return err
	}
	defer func() {
		for _, l := range logs {
			_ = l.f.Close()
		}
	}()
	found := req.Since == nil || req.Since.ID == ""
	for i, l := range logs {
		live := i == len(logs)-1
		if i > 0 {
			if err := validateNext(logs[i-1], l.header); err != nil {
				return err
			}
		}
		if req.Since == nil && live {
			continue
		}
		for {
			ev, err := readEvent(l.r, &l.pending)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			l.lastID = ev.ID
			if req.Since != nil && req.Since.ID != "" && !found {
				if ev.ID == req.Since.ID {
					found = true
				}
				continue
			}
			if req.Since != nil && req.Since.Time != nil && ev.TimeNano < req.Since.Time.UnixNano() {
				continue
			}
			if err := emitUntil(ev, req.Until, deliver); err != nil {
				return err
			}
		}
	}
	if !found {
		return ErrCursorUnavailable
	}
	return r.tail(ctx, logs[len(logs)-1], req.Until, deliver)
}

func (r *Reader) snapshot(includeHistory bool) ([]*openLog, error) {
	unlock, err := lock(r.lock)
	if err != nil {
		return nil, err
	}
	defer unlock()
	paths := []string{r.path}
	if includeHistory {
		paths = []string{r.path + ".1", r.path}
	}
	logs := []*openLog{}
	for _, path := range paths {
		f, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			closeLogs(logs)
			return nil, err
		}
		br := bufio.NewReader(f)
		h, err := readHeader(br)
		if err != nil {
			_ = f.Close()
			closeLogs(logs)
			return nil, err
		}
		if !includeHistory {
			if _, err := f.Seek(0, io.SeekEnd); err != nil {
				_ = f.Close()
				closeLogs(logs)
				return nil, err
			}
			br.Reset(f)
		}
		logs = append(logs, &openLog{f: f, r: br, header: h})
	}
	if len(logs) == 0 {
		return nil, os.ErrNotExist
	}
	return logs, nil
}

func closeLogs(logs []*openLog) {
	for _, l := range logs {
		_ = l.f.Close()
	}
}

func validateNext(previous *openLog, next header) error {
	if next.StreamID != previous.header.StreamID || next.PreviousGeneration == nil || *next.PreviousGeneration != previous.header.Generation {
		return ErrHistoryGap
	}
	if previous.lastID != "" && next.PreviousEventID != previous.lastID {
		return ErrHistoryGap
	}
	return nil
}

func readEvent(r *bufio.Reader, pending *[]byte) (Event, error) {
	line, err := r.ReadBytes('\n')
	if err != nil {
		if errors.Is(err, io.EOF) {
			*pending = append(*pending, line...)
			if len(*pending) > maxRecordSize {
				return Event{}, fmt.Errorf("event record exceeds %d bytes", maxRecordSize)
			}
			return Event{}, io.EOF
		}
		return Event{}, err
	}
	if len(*pending) > 0 {
		line = append(*pending, line...)
		*pending = (*pending)[:0]
	}
	if len(line) > maxRecordSize {
		return Event{}, fmt.Errorf("event record exceeds %d bytes", maxRecordSize)
	}
	var ev Event
	if err := json.Unmarshal(line, &ev); err != nil {
		return Event{}, fmt.Errorf("decode event: %w", err)
	}
	if ev.RecordType != "event" || ev.ID == "" {
		return Event{}, errors.New("invalid event record")
	}
	if ev.SchemaVersion != SchemaVersion {
		return Event{}, fmt.Errorf("unsupported event schema version %d", ev.SchemaVersion)
	}
	return ev, nil
}

func emitUntil(ev Event, until *time.Time, deliver func(Event) error) error {
	if until != nil && ev.TimeNano > until.UnixNano() {
		return nil
	}
	return deliver(ev)
}

func (r *Reader) waitForLog(ctx context.Context, req Request, deliver func(Event) error) error {
	ticker := time.NewTicker(r.poll)
	defer ticker.Stop()
	for {
		if req.Until != nil && !time.Now().Before(*req.Until) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := os.Stat(r.path); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return err
			}
			if req.Since == nil {
				oldest := time.Unix(0, 0)
				req.Since = &Since{Time: &oldest}
			}
			return r.Stream(ctx, req, deliver)
		}
	}
}

func (r *Reader) tail(ctx context.Context, current *openLog, until *time.Time, deliver func(Event) error) error {
	ticker := time.NewTicker(r.poll)
	defer ticker.Stop()
	for {
		for {
			ev, err := readEvent(current.r, &current.pending)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			current.lastID = ev.ID
			if err := emitUntil(ev, until, deliver); err != nil {
				return err
			}
		}
		if until != nil && !time.Now().Before(*until) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		rotated, err := pathChanged(current.f, r.path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if !rotated {
			continue
		}
		for {
			ev, err := readEvent(current.r, &current.pending)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			current.lastID = ev.ID
			if err := emitUntil(ev, until, deliver); err != nil {
				return err
			}
		}
		if len(current.pending) != 0 {
			return errors.New("event log rotated with an incomplete record")
		}
		next, err := r.openCurrent()
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err := validateNext(current, next.header); err != nil {
			_ = next.f.Close()
			return err
		}
		_ = current.f.Close()
		*current = *next
	}
}

func (r *Reader) openCurrent() (*openLog, error) {
	unlock, err := lock(r.lock)
	if err != nil {
		return nil, err
	}
	defer unlock()
	f, err := os.Open(r.path)
	if err != nil {
		return nil, err
	}
	br := bufio.NewReader(f)
	h, err := readHeader(br)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &openLog{f: f, r: br, header: h}, nil
}

func pathChanged(open *os.File, path string) (bool, error) {
	a, err := open.Stat()
	if err != nil {
		return false, err
	}
	b, err := os.Stat(path)
	if err != nil {
		return true, err
	}
	if !os.SameFile(a, b) {
		return true, nil
	}
	offset, err := open.Seek(0, io.SeekCurrent)
	if err != nil {
		return false, err
	}
	return b.Size() < offset, nil
}

// ParseMoment accepts RFC3339 timestamps, Unix seconds, and Go durations.
func ParseMoment(value string, now time.Time) (time.Time, error) {
	if d, err := time.ParseDuration(value); err == nil {
		return now.Add(-d), nil
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		return time.Unix(seconds, 0), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid time %q", value)
}

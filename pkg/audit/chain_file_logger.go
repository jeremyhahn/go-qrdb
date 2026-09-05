// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package audit

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/util/fsync"
)

// ChainFileOption configures a ChainFileLogger.
type ChainFileOption func(*ChainFileLogger)

// WithMaxBytes sets the maximum file size before rotation.
// Default: 100 MiB.
func WithMaxBytes(n int64) ChainFileOption {
	return func(l *ChainFileLogger) { l.maxBytes = n }
}

// WithRetentionDays sets the number of days to retain rotated files.
// Files older than this are deleted on startup. Default: 90.
func WithRetentionDays(days int) ChainFileOption {
	return func(l *ChainFileLogger) { l.retentionDays = days }
}

// WithSyncPerWrite enables fsync after every write (compliance mode).
// Default: true.
func WithSyncPerWrite(v bool) ChainFileOption {
	return func(l *ChainFileLogger) { l.syncPerWrite = v }
}

// ChainFileLogger is an append-only NDJSON file logger with SHA-256
// hash chaining for tamper detection, daily rotation, configurable
// maximum file size, and configurable retention policy.
//
// Hash-chain design:
//   - prevDigest starts as 32 zero bytes for the first event in each file.
//   - For each event: canonicalJSON = MarshalCanonical(event_without_prevHash);
//     currentDigest = SHA-256(prevDigest || canonicalJSON).
//   - event.PrevHash = hex(prevDigest) written to the NDJSON line.
//   - prevDigest advances to currentDigest.
//
// Rotation boundary:
//   - When the file exceeds maxBytes, the logger rotates to a new file named
//     audit-YYYY-MM-DD-NNN.ndjson. A "chain anchor" entry is appended to the
//     old file recording the final prevHash so that auditors can link the
//     chain across file boundaries.
//   - The new file starts with an empty prevHash (chain resets per file), but
//     the anchor event in the previous file records the handoff hash, enabling
//     cross-file verification when needed.
type ChainFileLogger struct {
	path          string
	maxBytes      int64
	retentionDays int
	syncPerWrite  bool

	mu         sync.Mutex
	f          *os.File
	prevDigest [sha256.Size]byte
	size       int64
}

// NewFileLogger opens the audit log file at path and returns a ChainFileLogger.
// On startup, rotated files older than retentionDays are deleted. The active
// log file is opened for append; the prevDigest is initialised by scanning
// the tail of the file.
func NewFileLogger(path string, opts ...ChainFileOption) (*ChainFileLogger, error) {
	l := &ChainFileLogger{
		path:          path,
		maxBytes:      100 * 1024 * 1024, // 100 MiB
		retentionDays: 90,
		syncPerWrite:  true,
	}
	for _, opt := range opts {
		opt(l)
	}

	if err := l.pruneOldFiles(); err != nil {
		slog.Error("audit: retention pruning failed", "error", err)
		// Non-fatal: log and continue.
	}

	if err := l.openFile(); err != nil {
		return nil, err
	}

	return l, nil
}

// openFile opens (or creates) the active audit log file and initialises
// prevDigest from the last event in the file.
func (l *ChainFileLogger) openFile() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o600)
	if err != nil {
		return &FileError{Path: l.path, Err: err}
	}
	l.f = f

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return &FileError{Path: l.path, Err: err}
	}
	l.size = info.Size()

	// Initialise prevDigest from the last written event.
	if l.size > 0 {
		if err := l.initPrevDigest(f); err != nil {
			// Non-fatal: chain will restart from zero on next write.
			slog.Error("audit: could not restore chain digest", "path", l.path, "error", err)
		}
	}

	return nil
}

// initPrevDigest scans the file to find the last valid event and re-derives
// prevDigest as SHA-256(prevDigest||canonicalJSON(event)) for each event,
// leaving prevDigest pointing at the digest after the last event.
func (l *ChainFileLogger) initPrevDigest(f *os.File) error {
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	defer func() { _, _ = f.Seek(0, 2) }() // reset to end

	scanner := bufio.NewScanner(f)
	var prev [sha256.Size]byte
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev AuditEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		canonical, err := canonicalJSON(ev)
		if err != nil {
			continue
		}
		prev = chainDigest(prev, canonical)
	}
	l.prevDigest = prev
	return nil
}

// Emit records the event to the NDJSON file with an updated hash chain.
// Emit is non-blocking and best-effort: errors are logged via slog but
// never returned to the caller.
func (l *ChainFileLogger) Emit(_ context.Context, ev AuditEvent) {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Compute canonical JSON without PrevHash, then derive the new digest.
	canonical, err := canonicalJSON(ev)
	if err != nil {
		slog.Error("audit: cannot marshal event for chain", "error", err)
		return
	}
	newDigest := chainDigest(l.prevDigest, canonical)

	// Set PrevHash to the previous digest (what the reader uses to verify).
	ev.PrevHash = hex.EncodeToString(l.prevDigest[:])
	if allZero(l.prevDigest) {
		ev.PrevHash = "" // first event: omit prevHash
	}

	line, err := json.Marshal(ev)
	if err != nil {
		slog.Error("audit: cannot marshal event", "error", err)
		return
	}
	line = append(line, '\n')

	// Rotate if the file would exceed maxBytes.
	if l.size+int64(len(line)) > l.maxBytes {
		if rotErr := l.rotate(); rotErr != nil {
			slog.Error("audit: rotation failed", "error", rotErr)
		}
	}

	n, err := l.f.Write(line)
	if err != nil {
		slog.Error("audit: write failed", "path", l.path, "error", err)
		return
	}
	l.size += int64(n)

	if l.syncPerWrite {
		if err := l.f.Sync(); err != nil {
			slog.Error("audit: fsync failed", "path", l.path, "error", err)
		}
	}

	l.prevDigest = newDigest
}

// Query scans the active file and returns events matching the filter.
// It opens a separate read-only file handle to avoid locking the writer.
func (l *ChainFileLogger) Query(_ context.Context, f Filter) (*Page, error) {
	f.Limit = normalizeLimit(f.Limit)

	l.mu.Lock()
	path := l.path
	l.mu.Unlock()

	events, err := scanFile(path, f)
	if err != nil {
		return nil, err
	}

	return buildPage(events, f), nil
}

// VerifyChain opens the file at path, recomputes the hash chain from the
// first event, and returns ChainBrokenError if any event's PrevHash does not
// match the expected value.
func (l *ChainFileLogger) VerifyChain(_ context.Context) error {
	l.mu.Lock()
	path := l.path
	l.mu.Unlock()

	return verifyChainFile(path)
}

// Close flushes and closes the underlying file.
func (l *ChainFileLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

// rotate moves the current file to a timestamped archive name, appends
// a chain-anchor entry to the old file, and opens a fresh file.
// Must be called with l.mu held.
func (l *ChainFileLogger) rotate() error {
	// Append chain anchor to the old file before rotating.
	anchor := AuditEvent{
		Timestamp: time.Now().UTC(),
		Actor:     "system",
		Resource:  "audit",
		Action:    "chain-anchor",
		Outcome:   OutcomeSuccess,
		PrevHash:  hex.EncodeToString(l.prevDigest[:]),
		Attributes: map[string]any{
			"note": "rotation boundary; next file starts a new chain; verify cross-file chain via prevHash in this entry",
		},
	}
	anchorLine, _ := json.Marshal(anchor)
	anchorLine = append(anchorLine, '\n')
	_, _ = l.f.Write(anchorLine)
	_ = l.f.Sync()

	if err := l.f.Close(); err != nil {
		return &FileError{Path: l.path, Err: err}
	}

	// Rename to archive, durably. The rename IS the rotation's commit point:
	// after it, the chain-anchor entry that closes the old file lives only
	// under the archive name. A crash that loses the directory entry leaves the
	// anchored file reachable under l.path, where openFile appends the new
	// chain onto it -- two chains in one file, with the anchor in the middle
	// claiming a boundary the reader cannot locate (go-qrdb#268).
	archivePath := rotatedPath(l.path)
	rotErr := fsync.Rename(l.path, archivePath)

	// Reset chain state for the new file. openFile overwrites both from the
	// file it finds, so this is correct whether the rename moved the old file
	// away or left it in place.
	var zero [sha256.Size]byte
	l.prevDigest = zero
	l.size = 0

	// Reopen UNCONDITIONALLY. l.f was closed above, so returning early on a
	// rotation failure would leave the logger with a nil file and every
	// subsequent Emit silently failing to write -- an audit logger that stops
	// auditing on a transient rename or fsync error. Report the rotation
	// failure, but not by disabling the log.
	openErr := l.openFile()
	if rotErr != nil {
		// Join rather than pick: when the reopen fails too, the logger is both
		// undurable AND dead, and reporting only the first hides the second.
		return &FileError{Path: l.path, Err: errors.Join(rotErr, openErr)}
	}
	return openErr
}

// pruneOldFiles deletes rotated audit files older than retentionDays.
func (l *ChainFileLogger) pruneOldFiles() error {
	dir := filepath.Dir(l.path)
	base := filepath.Base(l.path)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return &RetentionError{Err: err}
	}

	cutoff := time.Now().AddDate(0, 0, -l.retentionDays)
	for _, e := range entries {
		name := e.Name()
		if name == base {
			continue // skip active file
		}
		if !strings.HasPrefix(name, stem+"-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
				slog.Error("audit: failed to prune rotated file", "name", name, "error", err)
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Package-level helpers
// ---------------------------------------------------------------------------

// rotatedPath returns an archive path derived from path, stamped with the
// current date and a monotonic counter to avoid collisions.
func rotatedPath(path string) string {
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(path, ext)
	stamp := time.Now().UTC().Format("2006-01-02")
	for i := 0; i < 1000; i++ {
		candidate := fmt.Sprintf("%s-%s-%03d%s", stem, stamp, i, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	// Fallback: use nanoseconds to avoid collision.
	return fmt.Sprintf("%s-%s-%d%s", stem, stamp, time.Now().UnixNano(), ext)
}

// canonicalJSON serialises ev without the PrevHash field for digest computation.
// The serialisation uses standard encoding/json; field order is determined by
// the struct field order which is stable.
func canonicalJSON(ev AuditEvent) ([]byte, error) {
	// Strip PrevHash for canonical computation.
	type canonical struct {
		Timestamp  time.Time      `json:"timestamp"`
		Actor      string         `json:"actor"`
		TenantID   string         `json:"tenantID,omitempty"`
		Role       string         `json:"role,omitempty"`
		Resource   string         `json:"resource"`
		Action     string         `json:"action"`
		Outcome    string         `json:"outcome"`
		Attributes map[string]any `json:"attributes,omitempty"`
		ClientIP   string         `json:"clientIP,omitempty"`
		RequestID  string         `json:"requestID,omitempty"`
	}
	c := canonical{
		Timestamp:  ev.Timestamp,
		Actor:      ev.Actor,
		TenantID:   ev.TenantID,
		Role:       ev.Role,
		Resource:   ev.Resource,
		Action:     ev.Action,
		Outcome:    ev.Outcome,
		Attributes: ev.Attributes,
		ClientIP:   ev.ClientIP,
		RequestID:  ev.RequestID,
	}
	return json.Marshal(c)
}

// chainDigest computes SHA-256(prev || data).
func chainDigest(prev [sha256.Size]byte, data []byte) [sha256.Size]byte {
	h := sha256.New()
	h.Write(prev[:])
	h.Write(data)
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

// allZero returns true if all bytes in b are zero.
func allZero(b [sha256.Size]byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// scanFile opens the NDJSON file at path and returns all events matching f.
func scanFile(path string, f Filter) ([]AuditEvent, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, &FileError{Path: path, Err: err}
	}
	defer func() { _ = file.Close() }()

	var events []AuditEvent
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev AuditEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if matchesFilter(ev, f) {
			events = append(events, ev)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, &FileError{Path: path, Err: err}
	}
	return events, nil
}

// matchesFilter returns true if ev satisfies all non-zero filter fields.
func matchesFilter(ev AuditEvent, f Filter) bool {
	if !f.From.IsZero() && ev.Timestamp.Before(f.From) {
		return false
	}
	if !f.To.IsZero() && !ev.Timestamp.Before(f.To) {
		return false
	}
	if f.Actor != "" && ev.Actor != f.Actor {
		return false
	}
	if f.Resource != "" && ev.Resource != f.Resource {
		return false
	}
	if f.Action != "" && ev.Action != f.Action {
		return false
	}
	if f.Outcome != "" && !strings.HasPrefix(ev.Outcome, f.Outcome) {
		return false
	}
	if f.TenantID != "" && ev.TenantID != f.TenantID {
		return false
	}
	return true
}

// buildPage applies cursor pagination and constructs a Page from events.
func buildPage(events []AuditEvent, f Filter) *Page {
	// Sort by timestamp ascending for stable pagination.
	sort.Slice(events, func(i, j int) bool {
		return events[i].Timestamp.Before(events[j].Timestamp)
	})

	// Apply cursor: skip events before the cursor timestamp.
	start := 0
	if f.Cursor != "" {
		for i, ev := range events {
			ts := ev.Timestamp.UTC().Format(time.RFC3339Nano)
			if ts == f.Cursor {
				start = i + 1
				break
			}
		}
	}
	events = events[start:]

	total := len(events)

	limit := normalizeLimit(f.Limit)
	var next string
	if len(events) > limit {
		next = events[limit-1].Timestamp.UTC().Format(time.RFC3339Nano)
		events = events[:limit]
	}

	return &Page{
		Events: events,
		Next:   next,
		Total:  total,
	}
}

// verifyChainFile opens path and walks every event, recomputing the hash
// chain. Returns ChainBrokenError on the first mismatch.
func verifyChainFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return &FileError{Path: path, Err: err}
	}
	defer func() { _ = file.Close() }()

	var prev [sha256.Size]byte
	index := 0
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev AuditEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			index++
			continue
		}

		// Determine expected prevHash.
		var expectedPrevHash string
		if !allZero(prev) || index > 0 {
			expectedPrevHash = hex.EncodeToString(prev[:])
		}

		if ev.PrevHash != expectedPrevHash {
			return &ChainBrokenError{
				Index:    index,
				Expected: expectedPrevHash,
				Got:      ev.PrevHash,
			}
		}

		canonical, err := canonicalJSON(ev)
		if err != nil {
			return &ChainBrokenError{Index: index, Expected: "valid json", Got: "marshal error"}
		}
		prev = chainDigest(prev, canonical)
		index++
	}
	return nil
}

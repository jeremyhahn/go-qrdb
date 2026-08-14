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
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testEvent(actor, resource, action string) AuditEvent {
	return AuditEvent{
		Timestamp: time.Now().UTC(),
		Actor:     actor,
		TenantID:  "tenant-test",
		Role:      "admin",
		Resource:  resource,
		Action:    action,
		Outcome:   OutcomeSuccess,
		ClientIP:  "127.0.0.1",
		RequestID: "req-001",
	}
}

func TestChainFileLogger_EmitAndQuery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	defer func() { _ = l.Close() }()

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		l.Emit(ctx, testEvent("alice", "barrier", "unseal"))
	}

	page, err := l.Query(ctx, Filter{Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 5 {
		t.Fatalf("expected 5 events, got %d", len(page.Events))
	}
}

func TestChainFileLogger_HashChainIntegrity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}

	ctx := context.Background()
	for i := 0; i < 10; i++ {
		l.Emit(ctx, testEvent("bob", "ceremony", "initialize"))
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := verifyChainFile(path); err != nil {
		t.Fatalf("VerifyChain: unexpected error: %v", err)
	}
}

func TestChainFileLogger_VerifyChain_Tamper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		l.Emit(ctx, testEvent("charlie", "key", "create"))
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Read all lines, tamper line 3 (index 2), rewrite.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) < 4 {
		t.Fatalf("need at least 4 lines, got %d", len(lines))
	}

	// Mutate action in line at index 2.
	var ev AuditEvent
	if err := json.Unmarshal([]byte(lines[2]), &ev); err != nil {
		t.Fatalf("unmarshal line 2: %v", err)
	}
	ev.Action = "tampered"
	tampered, _ := json.Marshal(ev)
	lines[2] = string(tampered)

	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	err = verifyChainFile(path)
	if err == nil {
		t.Fatal("expected ChainBrokenError, got nil")
	}
	var chainErr *ChainBrokenError
	if !errors.As(err, &chainErr) {
		t.Fatalf("expected *ChainBrokenError, got %T: %v", err, err)
	}
}

func TestChainFileLogger_FilterByActor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	defer func() { _ = l.Close() }()

	ctx := context.Background()
	l.Emit(ctx, testEvent("alice", "barrier", "init"))
	l.Emit(ctx, testEvent("bob", "barrier", "init"))
	l.Emit(ctx, testEvent("alice", "ceremony", "unseal"))

	page, err := l.Query(ctx, Filter{Actor: "alice", Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("expected 2 events for alice, got %d", len(page.Events))
	}
	for _, ev := range page.Events {
		if ev.Actor != "alice" {
			t.Fatalf("expected actor alice, got %s", ev.Actor)
		}
	}
}

func TestChainFileLogger_FilterByTimeRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	defer func() { _ = l.Close() }()

	ctx := context.Background()
	t0 := time.Now().UTC()

	for i := 0; i < 3; i++ {
		ev := testEvent("dave", "kv", "put")
		ev.Timestamp = t0.Add(time.Duration(i) * time.Second)
		l.Emit(ctx, ev)
	}

	page, err := l.Query(ctx, Filter{
		From:  t0.Add(time.Second),
		To:    t0.Add(3 * time.Second),
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("expected 2 events in range, got %d", len(page.Events))
	}
}

func TestChainFileLogger_FilterByOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	defer func() { _ = l.Close() }()

	ctx := context.Background()
	ev1 := testEvent("eve", "barrier", "unseal")
	ev1.Outcome = OutcomeSuccess
	ev2 := testEvent("eve", "barrier", "unseal")
	ev2.Outcome = "failure:bad-key"
	l.Emit(ctx, ev1)
	l.Emit(ctx, ev2)

	page, err := l.Query(ctx, Filter{Outcome: "failure", Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("expected 1 failure event, got %d", len(page.Events))
	}
}

func TestChainFileLogger_Pagination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	defer func() { _ = l.Close() }()

	ctx := context.Background()
	base := time.Now().UTC()
	for i := 0; i < 10; i++ {
		ev := testEvent("frank", "kv", "get")
		ev.Timestamp = base.Add(time.Duration(i) * time.Millisecond)
		l.Emit(ctx, ev)
	}

	// First page: 4 events.
	page1, err := l.Query(ctx, Filter{Limit: 4})
	if err != nil {
		t.Fatalf("Query page1: %v", err)
	}
	if len(page1.Events) != 4 {
		t.Fatalf("page1: expected 4, got %d", len(page1.Events))
	}
	if page1.Next == "" {
		t.Fatal("expected non-empty Next cursor")
	}

	// Second page using cursor.
	page2, err := l.Query(ctx, Filter{Limit: 4, Cursor: page1.Next})
	if err != nil {
		t.Fatalf("Query page2: %v", err)
	}
	if len(page2.Events) != 4 {
		t.Fatalf("page2: expected 4, got %d", len(page2.Events))
	}
}

func TestChainFileLogger_ConcurrentEmit_NoRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	defer func() { _ = l.Close() }()

	ctx := context.Background()
	const goroutines = 20
	const eventsEach = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < eventsEach; j++ {
				l.Emit(ctx, testEvent("worker", "kv", "put"))
			}
		}(i)
	}
	wg.Wait()

	page, err := l.Query(ctx, Filter{Limit: 1000})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if page.Total < goroutines*eventsEach {
		// All events should be present.
		t.Fatalf("expected at least %d events, got %d", goroutines*eventsEach, page.Total)
	}
}

func TestChainFileLogger_NDJSONFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}

	ctx := context.Background()
	l.Emit(ctx, testEvent("grace", "ceremony", "claim-share"))
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// Must end with newline.
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatal("expected NDJSON file to end with newline")
	}
	// Each line must be valid JSON.
	scanner := bufio.NewScanner(strings.NewReader(strings.TrimSpace(string(data))))
	for scanner.Scan() {
		var m map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &m); err != nil {
			t.Fatalf("line is not valid JSON: %v", err)
		}
	}
}

func TestChainFileLogger_Close(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Second close is a no-op.
	if err := l.Close(); err != nil {
		t.Fatalf("double Close: %v", err)
	}
}

func TestChainFileLogger_Rotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.ndjson")
	// Set a very small max size to force rotation.
	l, err := NewFileLogger(path, WithMaxBytes(512))
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	defer func() { _ = l.Close() }()

	ctx := context.Background()
	// Emit enough events to trigger rotation.
	for i := 0; i < 50; i++ {
		l.Emit(ctx, testEvent("henry", "kv", "delete"))
	}

	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	// Should have at least 2 files (active + at least one rotated).
	if len(entries) < 2 {
		t.Fatalf("expected at least 2 files after rotation, got %d", len(entries))
	}
}

func TestChainFileLogger_Retention(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")

	// Create an old rotated file.
	oldPath := filepath.Join(dir, "audit-2020-01-01-000.ndjson")
	if err := os.WriteFile(oldPath, []byte(`{"action":"test"}`+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Make it appear old.
	old := time.Now().AddDate(-2, 0, 0)
	if err := os.Chtimes(oldPath, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	_, err := NewFileLogger(path, WithRetentionDays(30))
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}

	// Old file should be deleted.
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("expected old rotated file to be pruned; stat returned: %v", err)
	}
}

func TestChainFileLogger_ImplementsAuditLogger(t *testing.T) {
	var _ AuditLogger = (*ChainFileLogger)(nil)
}

func TestChainFileLogger_WithSyncPerWrite_False(t *testing.T) {
	// WithSyncPerWrite(false) must not panic and must still emit events correctly.
	path := filepath.Join(t.TempDir(), "audit.ndjson")
	l, err := NewFileLogger(path, WithSyncPerWrite(false))
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	defer func() { _ = l.Close() }()

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		l.Emit(ctx, testEvent("sync-off", "kv", "put"))
	}
	page, err := l.Query(ctx, Filter{Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 5 {
		t.Fatalf("expected 5 events, got %d", len(page.Events))
	}
}

func TestChainFileLogger_VerifyChain_ViaMethod(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < 8; i++ {
		l.Emit(ctx, testEvent("verify", "ceremony", "check"))
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-open to get a new logger pointing at the same path.
	l2, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger (re-open): %v", err)
	}
	defer func() { _ = l2.Close() }()

	if err := l2.VerifyChain(ctx); err != nil {
		t.Fatalf("VerifyChain: unexpected error: %v", err)
	}
}

func TestChainFileLogger_InitPrevDigest_Restores(t *testing.T) {
	// Emitting events, closing, and re-opening should restore the chain.
	path := filepath.Join(t.TempDir(), "audit.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		l.Emit(ctx, testEvent("restore", "barrier", "init"))
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-open and emit more events — chain must remain unbroken.
	l2, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger (re-open): %v", err)
	}
	for i := 0; i < 3; i++ {
		l2.Emit(ctx, testEvent("restore", "barrier", "unseal"))
	}
	if err := l2.Close(); err != nil {
		t.Fatalf("Close (l2): %v", err)
	}

	if err := verifyChainFile(path); err != nil {
		t.Fatalf("verifyChainFile after re-open: %v", err)
	}
}

func TestChainFileLogger_FilterByResource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	defer func() { _ = l.Close() }()

	ctx := context.Background()
	l.Emit(ctx, testEvent("alice", "barrier", "init"))
	l.Emit(ctx, testEvent("alice", "kv", "put"))
	l.Emit(ctx, testEvent("alice", "barrier", "unseal"))

	page, err := l.Query(ctx, Filter{Resource: "barrier", Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("expected 2 barrier events, got %d", len(page.Events))
	}
}

func TestChainFileLogger_FilterByAction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	defer func() { _ = l.Close() }()

	ctx := context.Background()
	l.Emit(ctx, testEvent("bob", "barrier", "init"))
	l.Emit(ctx, testEvent("bob", "kv", "put"))
	l.Emit(ctx, testEvent("bob", "kv", "get"))

	page, err := l.Query(ctx, Filter{Action: "put", Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("expected 1 put event, got %d", len(page.Events))
	}
}

func TestChainFileLogger_Emit_ZeroTimestamp(t *testing.T) {
	// Emit with zero timestamp should fill in current time.
	path := filepath.Join(t.TempDir(), "audit.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	defer func() { _ = l.Close() }()

	ctx := context.Background()
	ev := AuditEvent{
		Actor:    "zero-ts",
		Resource: "kv",
		Action:   "put",
		Outcome:  OutcomeSuccess,
		// Timestamp deliberately zero
	}
	l.Emit(ctx, ev)

	page, err := l.Query(ctx, Filter{Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(page.Events))
	}
	if page.Events[0].Timestamp.IsZero() {
		t.Fatal("expected non-zero timestamp after emit")
	}
}

func TestChainFileLogger_FilterByTenantID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	defer func() { _ = l.Close() }()

	ctx := context.Background()
	ev1 := testEvent("carol", "kv", "put")
	ev1.TenantID = "tenant-a"
	ev2 := testEvent("carol", "kv", "put")
	ev2.TenantID = "tenant-b"
	l.Emit(ctx, ev1)
	l.Emit(ctx, ev2)

	page, err := l.Query(ctx, Filter{TenantID: "tenant-a", Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("expected 1 tenant-a event, got %d", len(page.Events))
	}
}

func TestChainFileLogger_Query_NonexistentFile(t *testing.T) {
	// Query on a path that was never created should return an empty page.
	path := filepath.Join(t.TempDir(), "nonexistent.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	defer func() { _ = l.Close() }()

	// Remove the file so it doesn't exist when Query is called.
	_ = l.Close()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatalf("Remove: %v", err)
	}

	l2, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger (re-open): %v", err)
	}
	defer func() { _ = l2.Close() }()

	// Temporarily rename the active file to force scanFile to see nonexistent.
	_ = l2.Close()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatalf("Remove path: %v", err)
	}
	// Create a fresh logger pointing at the removed path — this will create a new file.
	// To truly test scanFile with nonexistent path, call it directly.
	events, err := scanFile(filepath.Join(t.TempDir(), "does-not-exist.ndjson"), Filter{Limit: 10})
	if err != nil {
		t.Fatalf("scanFile on nonexistent path: expected nil error, got %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected 0 events from nonexistent file, got %d", len(events))
	}
}

func TestChainFileLogger_PruneOldFiles_NonexistentDir(t *testing.T) {
	// If the directory doesn't exist, pruneOldFiles should not error.
	path := filepath.Join(t.TempDir(), "subdir", "audit.ndjson")
	// subdir does not exist yet; pruneOldFiles should handle os.IsNotExist gracefully
	// and then NewFileLogger should create the directory via openFile.
	// Note: os.OpenFile will fail if the directory doesn't exist;
	// so we pre-create the dir to test the nonexistent dir on pruning.
	subdir := filepath.Dir(path)
	if err := os.MkdirAll(subdir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	l := &ChainFileLogger{
		path:          path,
		maxBytes:      100 * 1024 * 1024,
		retentionDays: 30,
		syncPerWrite:  true,
	}
	// Remove the subdir to trigger the os.IsNotExist path.
	if err := os.Remove(subdir); err != nil {
		t.Fatalf("Remove subdir: %v", err)
	}
	// pruneOldFiles should return nil for nonexistent directory.
	if err := l.pruneOldFiles(); err != nil {
		t.Fatalf("pruneOldFiles on nonexistent dir: expected nil, got %v", err)
	}
}

func TestChainFileLogger_Emit_WithAttributes(t *testing.T) {
	// Emit events with attributes map to cover slog Attributes branch.
	path := filepath.Join(t.TempDir(), "audit.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	defer func() { _ = l.Close() }()

	ctx := context.Background()
	ev := testEvent("attr-actor", "barrier", "init")
	ev.Attributes = map[string]any{"key": "value", "count": 42}
	ev.TenantID = "tenant-x"
	ev.Role = "operator"
	ev.ClientIP = "10.0.0.1"
	ev.RequestID = "req-xyz"
	l.Emit(ctx, ev)

	page, err := l.Query(ctx, Filter{Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(page.Events))
	}
}

func TestVerifyChainFile_NonexistentFile(t *testing.T) {
	err := verifyChainFile(filepath.Join(t.TempDir(), "missing.ndjson"))
	if err == nil {
		t.Fatal("expected error for nonexistent file, got nil")
	}
	var fe *FileError
	if !errors.As(err, &fe) {
		t.Fatalf("expected *FileError, got %T: %v", err, err)
	}
}

func TestChainFileLogger_Emit_AfterFileClose(t *testing.T) {
	// Force the underlying fd to be closed before Emit to cover the write error path.
	path := filepath.Join(t.TempDir(), "audit.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	// Close the underlying file without going through l.Close() to leave l.f non-nil.
	l.mu.Lock()
	_ = l.f.Close()
	l.mu.Unlock()

	ctx := context.Background()
	// Emit must not panic even when the file descriptor is closed.
	l.Emit(ctx, testEvent("broken", "kv", "put"))
	// l.f is now in a broken state; clean up by zeroing it.
	l.mu.Lock()
	l.f = nil
	l.mu.Unlock()
}

func TestChainFileLogger_Query_Cursor_NoMatch(t *testing.T) {
	// A cursor that matches no timestamp should start from the beginning.
	path := filepath.Join(t.TempDir(), "audit.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	defer func() { _ = l.Close() }()

	ctx := context.Background()
	base := time.Now().UTC()
	for i := 0; i < 5; i++ {
		ev := testEvent("frank", "kv", "get")
		ev.Timestamp = base.Add(time.Duration(i) * time.Millisecond)
		l.Emit(ctx, ev)
	}

	// Use a cursor that won't match any event timestamp.
	page, err := l.Query(ctx, Filter{Limit: 10, Cursor: "1999-01-01T00:00:00Z"})
	if err != nil {
		t.Fatalf("Query with non-matching cursor: %v", err)
	}
	// All events should be returned since cursor doesn't match.
	if len(page.Events) != 5 {
		t.Fatalf("expected 5 events with unmatched cursor, got %d", len(page.Events))
	}
}

func TestVerifyChainFile_SkipsInvalidJSON(t *testing.T) {
	// A file with invalid JSON lines should skip them without error.
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	ctx := context.Background()
	l.Emit(ctx, testEvent("skip", "kv", "put"))
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Append an invalid JSON line after the valid events.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	_, _ = f.WriteString("not-valid-json\n")
	_ = f.Close()

	// verifyChainFile should skip the invalid line and not return ChainBrokenError.
	if err := verifyChainFile(path); err != nil {
		t.Fatalf("verifyChainFile: unexpected error on invalid JSON line: %v", err)
	}
}

func TestScanFile_SkipsInvalidJSON(t *testing.T) {
	// scanFile should skip invalid JSON lines and return valid events.
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	ctx := context.Background()
	l.Emit(ctx, testEvent("scan", "kv", "put"))
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Append an invalid JSON line.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	_, _ = f.WriteString("not-valid-json\n")
	_ = f.Close()

	events, err := scanFile(path, Filter{Limit: 10})
	if err != nil {
		t.Fatalf("scanFile: unexpected error: %v", err)
	}
	// The valid event should be returned; the invalid line is skipped.
	if len(events) != 1 {
		t.Fatalf("expected 1 valid event, got %d", len(events))
	}
}

func TestChainFileLogger_Retention_NoOldFiles(t *testing.T) {
	// Retention with no old files in a directory that has only the active log.
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")
	l, err := NewFileLogger(path, WithRetentionDays(1))
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	defer func() { _ = l.Close() }()

	ctx := context.Background()
	l.Emit(ctx, testEvent("recent", "kv", "put"))
	// No pruning errors expected.
	page, err := l.Query(ctx, Filter{Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(page.Events))
	}
}

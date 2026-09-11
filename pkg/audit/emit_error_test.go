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

// Emit's error contract (go-qrdb#335).
//
// No build constraint, deliberately: these are the tests for a security
// property enforced in every build, and the helper below is used from this
// file only. A constraint here would delete the proof from the tag sets that
// still ship the behaviour.

package audit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/util/metrics"
)

// failingSink fails Emit a configurable number of times and counts calls, so
// a test can assert both "this sink refused" and "this sink was still
// reached" -- the second being the property the aggregate fan-out exists for.
type failingSink struct {
	name  string
	err   error
	calls int
}

func (f *failingSink) Emit(_ context.Context, _ AuditEvent) error {
	f.calls++
	return f.err
}

func (f *failingSink) Query(_ context.Context, _ Filter) (*Page, error) {
	return &Page{Total: -1}, nil
}

func (f *failingSink) Close() error { return nil }

var _ AuditLogger = (*failingSink)(nil)

// ---------------------------------------------------------------------------
// The sinks that cannot fail say so
// ---------------------------------------------------------------------------

func TestNoopLogger_Emit_ReturnsNil(t *testing.T) {
	if err := NewNoopLogger().Emit(context.Background(), AuditEvent{Actor: "a"}); err != nil {
		t.Fatalf("NoopLogger.Emit must report success: %v", err)
	}
}

// TestNoopLogger_Emit_NilIsTheTruthfulAnswer pins the reasoning, not just the
// value: an operator who configured no audit backend chose to keep no trail,
// and a fail-closed middleware over a NoopLogger must still serve requests.
// A NoopLogger that returned "could not record" would deny every request on
// every node that disables auditing.
func TestNoopLogger_Emit_NilIsTheTruthfulAnswer(t *testing.T) {
	n := NewNoopLogger()
	for i := 0; i < 100; i++ {
		if err := n.Emit(context.Background(), AuditEvent{Actor: "a"}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
}

func TestRingBuffer_Emit_ReturnsNil(t *testing.T) {
	r := NewRingBuffer(4)
	if err := r.Emit(context.Background(), AuditEvent{Actor: "a"}); err != nil {
		t.Fatalf("RingBuffer.Emit must report success: %v", err)
	}
}

// TestRingBuffer_Emit_OverwriteIsNotAFailure: dropping the oldest event when
// the ring wraps is the buffer's stated capacity policy, not a write failure.
// Reporting it as one would make a fail-closed middleware deny every request
// after the 4th on a 4-slot buffer.
func TestRingBuffer_Emit_OverwriteIsNotAFailure(t *testing.T) {
	r := NewRingBuffer(2)
	for i := 0; i < 10; i++ {
		if err := r.Emit(context.Background(), AuditEvent{Actor: "a"}); err != nil {
			t.Fatalf("emit %d after wrap: %v", i, err)
		}
	}
	if got := len(r.Snapshot()); got != 2 {
		t.Fatalf("expected the ring to hold 2 events, got %d", got)
	}
}

func TestSlogSink_Emit_ReturnsNil(t *testing.T) {
	if err := NewSlogSink().Emit(context.Background(), AuditEvent{Actor: "a"}); err != nil {
		t.Fatalf("SlogSink.Emit must report success: %v", err)
	}
}

// TestSlogSink_Emit_ReturnsNilWithCustomLogger covers the NewSlogSinkWith
// construction path as well, since that is what an embedder supplying its own
// handler actually runs.
func TestSlogSink_Emit_ReturnsNilWithCustomLogger(t *testing.T) {
	s := NewSlogSinkWith(slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, ev := range []AuditEvent{
		{Actor: "a", Resource: "kv", Action: "put", Outcome: OutcomeSuccess},
		{Actor: "b", TenantID: "t1", Role: "admin", ClientIP: "203.0.113.1", RequestID: "r1",
			Attributes: map[string]any{"k": "v"}},
	} {
		if err := s.Emit(context.Background(), ev); err != nil {
			t.Fatalf("SlogSink.Emit(%+v): %v", ev, err)
		}
	}
}

// ---------------------------------------------------------------------------
// ChainFileLogger: the sink that CAN fail, and the only producer of WriteError
// ---------------------------------------------------------------------------

func TestChainFileLogger_Emit_Success(t *testing.T) {
	l := newTempChainLogger(t)
	if err := l.Emit(context.Background(), AuditEvent{Actor: "a", Action: "put"}); err != nil {
		t.Fatalf("a healthy sink must report success: %v", err)
	}
}

// TestChainFileLogger_Emit_WriteFailure_AfterClose is the write-failure path
// WITHOUT a fault-injection hook: a closed sink is a real production race (the
// daemon closes the audit logger while in-flight requests drain) and it fails
// at the real boundary, os.File.Write.
func TestChainFileLogger_Emit_WriteFailure_AfterClose(t *testing.T) {
	l := newTempChainLogger(t)
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	err := l.Emit(context.Background(), AuditEvent{Actor: "a", Action: "put"})
	if err == nil {
		t.Fatal("expected a write failure from a closed sink")
	}
	var we *WriteError
	if !errors.As(err, &we) {
		t.Fatalf("expected *WriteError, got %T: %v", err, err)
	}
	if we.Backend != l.path {
		t.Errorf("WriteError.Backend = %q, want the failing file %q", we.Backend, l.path)
	}
	if we.Unwrap() == nil {
		t.Error("WriteError must carry the underlying cause")
	}
	if !strings.Contains(we.Error(), l.path) {
		t.Errorf("WriteError.Error() must name the backend, got %q", we.Error())
	}
}

// TestChainFileLogger_Emit_WriteFailure_Injected drives the same branch
// through the writeFunc seam, so the assertion holds for a disk that refuses
// the append rather than a handle that is gone.
func TestChainFileLogger_Emit_WriteFailure_Injected(t *testing.T) {
	l := newTempChainLogger(t)
	defer func() { _ = l.Close() }()

	diskFull := errors.New("no space left on device")
	l.writeFunc = func([]byte) (int, error) { return 0, diskFull }

	err := l.Emit(context.Background(), AuditEvent{Actor: "a", Action: "put"})
	var we *WriteError
	if !errors.As(err, &we) {
		t.Fatalf("expected *WriteError, got %T: %v", err, err)
	}
	if !errors.Is(err, diskFull) {
		t.Errorf("WriteError must wrap the disk's own error, got %v", err)
	}
}

// TestChainFileLogger_Emit_FsyncFailure covers the compliance-mode durability
// boundary. fsync cannot be made to fail portably from a test, which is the
// entire reason the syncFunc field exists.
func TestChainFileLogger_Emit_FsyncFailure(t *testing.T) {
	l := newTempChainLogger(t)
	defer func() { _ = l.Close() }()

	ioErr := errors.New("input/output error")
	l.syncFunc = func() error { return ioErr }

	err := l.Emit(context.Background(), AuditEvent{Actor: "a", Action: "put"})
	var we *WriteError
	if !errors.As(err, &we) {
		t.Fatalf("expected *WriteError, got %T: %v", err, err)
	}
	if !errors.Is(err, ioErr) {
		t.Errorf("WriteError must wrap the fsync error, got %v", err)
	}
}

// TestChainFileLogger_VerifyChain_SurvivesAnFsyncFailure is the regression
// test for the durable-state half of the fsync path, and it is the shape the
// single-emit test above cannot have: emit ACROSS the fault, then read back.
//
// The bug it pins: the append succeeds and the record IS in the file, then
// fsync fails. Returning the *WriteError before advancing prevDigest left the
// NEXT event linking to the pre-failure digest, so VerifyChain reported a
// broken chain on honest, untampered data -- the sink the authorization
// middleware is told to trust would have failed read-back verification of its
// own trail under the one fault this package injects.
//
// Emit still reports the failure. What it must not do is leave the record in
// the file with the chain pointed before it.
func TestChainFileLogger_VerifyChain_SurvivesAnFsyncFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")
	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer func() { _ = l.Close() }()

	ctx := context.Background()
	if err := l.Emit(ctx, AuditEvent{Actor: "a", Action: "before"}); err != nil {
		t.Fatalf("healthy emit: %v", err)
	}

	// The fault: the append lands, the fsync does not.
	ioErr := errors.New("input/output error")
	l.syncFunc = func() error { return ioErr }
	if emitErr := l.Emit(ctx, AuditEvent{Actor: "a", Action: "during"}); !errors.Is(emitErr, ioErr) {
		t.Fatalf("the fsync failure must still be reported, got %v", emitErr)
	}

	// The disk recovers and the sink keeps writing.
	l.syncFunc = nil
	if err := l.Emit(ctx, AuditEvent{Actor: "a", Action: "after"}); err != nil {
		t.Fatalf("emit after recovery: %v", err)
	}

	// All three records are present...
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read back: %v", readErr)
	}
	for _, want := range []string{"before", "during", "after"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("record %q missing from the trail: %q", want, string(data))
		}
	}

	// ...and the chain over them verifies. Before the fix this failed with
	// ChainBrokenError at index 2, because "after" carried the digest that
	// belonged before "during".
	if err := l.VerifyChain(ctx); err != nil {
		var broken *ChainBrokenError
		if errors.As(err, &broken) {
			t.Fatalf("chain broken at index %d on untampered data: expected prevHash %q, got %q",
				broken.Index, broken.Expected, broken.Got)
		}
		t.Fatalf("VerifyChain: %v", err)
	}
}

// TestChainFileLogger_VerifyChain_SurvivesAReopenAfterAnFsyncFailure is the
// crash half. The process dies after the unsynced write; the record happens to
// have survived. A fresh logger over the same file must land on a digest that
// continues the chain rather than breaking it.
//
// It works because initPrevDigest re-derives by FOLDING OVER THE RECORDS IT
// CAN READ BACK, not by trusting the last record's stored PrevHash. Had it
// done the latter, the surviving-record and lost-record cases would need
// different handling and this fix would not be sound.
func TestChainFileLogger_VerifyChain_SurvivesAReopenAfterAnFsyncFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")

	l, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	if err := l.Emit(ctx, AuditEvent{Actor: "a", Action: "before"}); err != nil {
		t.Fatalf("healthy emit: %v", err)
	}
	l.syncFunc = func() error { return errors.New("input/output error") }
	if emitErr := l.Emit(ctx, AuditEvent{Actor: "a", Action: "during"}); emitErr == nil {
		t.Fatal("expected the fsync failure to be reported")
	}
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen, as a restarted daemon would.
	l2, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = l2.Close() }()
	if err := l2.Emit(ctx, AuditEvent{Actor: "a", Action: "after-restart"}); err != nil {
		t.Fatalf("emit after restart: %v", err)
	}

	if err := l2.VerifyChain(ctx); err != nil {
		var broken *ChainBrokenError
		if errors.As(err, &broken) {
			t.Fatalf("chain broken at index %d after restart: expected prevHash %q, got %q",
				broken.Index, broken.Expected, broken.Got)
		}
		t.Fatalf("VerifyChain: %v", err)
	}
}

// TestChainFileLogger_VerifyChain_SurvivesAWriteFailure is the same property
// for the other fault: the write is REFUSED outright, so no record landed and
// the chain must NOT advance. Emitting again healthy must continue from where
// the trail actually is.
func TestChainFileLogger_VerifyChain_SurvivesAWriteFailure(t *testing.T) {
	l := newTempChainLogger(t)
	defer func() { _ = l.Close() }()

	ctx := context.Background()
	if err := l.Emit(ctx, AuditEvent{Actor: "a", Action: "before"}); err != nil {
		t.Fatalf("healthy emit: %v", err)
	}

	l.writeFunc = func([]byte) (int, error) { return 0, errors.New("no space left on device") }
	if emitErr := l.Emit(ctx, AuditEvent{Actor: "a", Action: "lost"}); emitErr == nil {
		t.Fatal("expected the write failure to be reported")
	}

	l.writeFunc = nil
	if err := l.Emit(ctx, AuditEvent{Actor: "a", Action: "after"}); err != nil {
		t.Fatalf("emit after recovery: %v", err)
	}

	if err := l.VerifyChain(ctx); err != nil {
		t.Fatalf("a refused write must leave the chain where it was: %v", err)
	}
}

// TestChainFileLogger_VerifyChain_SurvivesATornWrite: a SHORT write leaves
// bytes in the file that are not a record. prevDigest must not advance over
// them -- initPrevDigest and verifyChainFile both skip a line that will not
// unmarshal, so advancing would put the chain ahead of what either can see.
func TestChainFileLogger_VerifyChain_SurvivesATornWrite(t *testing.T) {
	l := newTempChainLogger(t)
	defer func() { _ = l.Close() }()

	ctx := context.Background()
	if err := l.Emit(ctx, AuditEvent{Actor: "a", Action: "before"}); err != nil {
		t.Fatalf("healthy emit: %v", err)
	}
	beforeDigest := l.prevDigest

	// Half the line reaches the disk, then the write errors.
	l.writeFunc = func(p []byte) (int, error) {
		return len(p) / 2, errors.New("short write")
	}
	if emitErr := l.Emit(ctx, AuditEvent{Actor: "a", Action: "torn"}); emitErr == nil {
		t.Fatal("expected the short write to be reported")
	}
	if l.prevDigest != beforeDigest {
		t.Fatal("the chain advanced over a torn line, which is not a record")
	}
}

// TestChainFileLogger_Emit_FsyncFailureSkippedWhenSyncPerWriteOff: with
// syncPerWrite disabled the operator chose not to fsync, so there is no fsync
// boundary to fail at and Emit reports success.
func TestChainFileLogger_Emit_FsyncFailureSkippedWhenSyncPerWriteOff(t *testing.T) {
	l := newTempChainLogger(t, WithSyncPerWrite(false))
	defer func() { _ = l.Close() }()

	l.syncFunc = func() error { return errors.New("would have failed") }

	if err := l.Emit(context.Background(), AuditEvent{Actor: "a"}); err != nil {
		t.Fatalf("no fsync was requested, so none could fail: %v", err)
	}
}

// TestChainFileLogger_Emit_RotationFailure_IsNotAnUnwrittenRecord.
//
// This test previously asserted the OPPOSITE -- that a rotation failure makes
// Emit return *WriteError. That was wrong, and wrong in the direction that
// matters: rotation runs BEFORE the append, so when it fails the append still
// runs, still fsyncs, and the record is on disk. Reporting it made the
// authorization middleware refuse a request whose audit entry had in fact
// persisted, contradicting the contract AuditLogger.Emit states.
//
// The record landing is asserted here as it was before. What changed is that
// Emit now says so.
func TestChainFileLogger_Emit_RotationFailure_IsNotAnUnwrittenRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")
	l, err := NewFileLogger(path, WithMaxBytes(1))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer func() { _ = l.Close() }()

	l.rotateFunc = func() error { return &FileError{Path: path, Err: errors.New("permission denied")} }

	if emitErr := l.Emit(context.Background(), AuditEvent{Actor: "a", Action: "rotate-me"}); emitErr != nil {
		t.Fatalf("the record is durable, so Emit must report success; got %v", emitErr)
	}

	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read back: %v", readErr)
	}
	if !strings.Contains(string(data), "rotate-me") {
		t.Fatalf("the record must survive a rotation failure; file held %q", string(data))
	}
}

// TestChainFileLogger_Emit_PersistentRotationFailure_DoesNotWedgeTheNode is
// the test the review named, and it pins the property that actually matters.
//
// rotate() reopens unconditionally and openFile re-stats the same oversized
// file, so l.size stays above maxBytes and the rotation is re-attempted
// forever. When Emit reported that failure, a directory that had become
// unwritable while the open handle stayed writable took the node to
// 503-on-every-authorized-request -- including the tenant-barrier unseal an
// operator would use to recover -- while the trail was healthy throughout.
// Recovery needed a config change and a restart.
//
// So: many events, a rotation that never succeeds, and every single one must
// still be reported as written AND be on disk.
func TestChainFileLogger_Emit_PersistentRotationFailure_DoesNotWedgeTheNode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")
	l, err := NewFileLogger(path, WithMaxBytes(1))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer func() { _ = l.Close() }()

	var attempts int
	l.rotateFunc = func() error {
		attempts++
		return errors.New("rename: permission denied")
	}

	const events = 25
	ctx := context.Background()
	for i := 0; i < events; i++ {
		if emitErr := l.Emit(ctx, AuditEvent{Actor: "a", Action: "evt", Resource: itoa(i)}); emitErr != nil {
			t.Fatalf("emit %d refused a durable record: %v", i, emitErr)
		}
	}

	// Every record landed.
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read back: %v", readErr)
	}
	if got := strings.Count(string(data), `"action":"evt"`); got != events {
		t.Fatalf("the trail holds %d of %d records", got, events)
	}

	// And the failure did not become a per-event full-file rescan plus a
	// chain-anchor line per event. One attempt inside the retry window.
	if attempts != 1 {
		t.Fatalf("rotation attempted %d times for %d events; a failed rotation must "+
			"be bounded, not retried on every write", attempts, events)
	}
}

// TestChainFileLogger_Emit_RotationRetriesAfterTheInterval: the bound is a
// backoff, not a permanent surrender. Once the window passes the sink tries
// again, so a fixed permission problem resumes rotating without a restart.
func TestChainFileLogger_Emit_RotationRetriesAfterTheInterval(t *testing.T) {
	l := newTempChainLogger(t, WithMaxBytes(1))
	defer func() { _ = l.Close() }()

	var attempts int
	l.rotateFunc = func() error {
		attempts++
		return errors.New("rename: permission denied")
	}

	ctx := context.Background()
	if err := l.Emit(ctx, AuditEvent{Actor: "a"}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}

	// Expire the window the way the clock would.
	l.rotateRetryAt = time.Now().Add(-time.Second)
	if err := l.Emit(ctx, AuditEvent{Actor: "a"}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2 -- the backoff must expire", attempts)
	}

	// A rotation that finally succeeds clears the outstanding failure.
	l.rotateFunc = nil
	l.rotateRetryAt = time.Now().Add(-time.Second)
	if err := l.Emit(ctx, AuditEvent{Actor: "a"}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if !l.rotateRetryAt.IsZero() {
		t.Fatal("a successful rotation must clear the outstanding-failure marker")
	}
}

// TestChainFileLogger_Emit_RotationFailure_IsCounted. The failure is real and
// must not become silent just because it stopped refusing requests -- a file
// that cannot rotate will eventually fail to write for real.
//
// The counter is SEPARATE from qrdb_audit_write_failures_total, which means
// "a record was lost". Nothing was lost here, and folding the two together
// would make the one an operator pages on lie.
func TestChainFileLogger_Emit_RotationFailure_IsCounted(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	before := rotationFailureCount(t)
	lostBefore := bestEffortFailureCount(t, "user")

	l := newTempChainLogger(t, WithMaxBytes(1))
	defer func() { _ = l.Close() }()
	l.rotateFunc = func() error { return errors.New("rename: permission denied") }

	if err := l.Emit(context.Background(), AuditEvent{Actor: "a"}); err != nil {
		t.Fatalf("emit: %v", err)
	}

	if after := rotationFailureCount(t); after != before+1 {
		t.Fatalf("qrdb_audit_rotation_failures_total = %v, want %v", after, before+1)
	}
	if after := bestEffortFailureCount(t, "user"); after != lostBefore {
		t.Error("a rotation failure must not be counted as a lost record")
	}
	if !strings.Contains(buf.String(), "rotation failed") {
		t.Errorf("the failure must reach the operational log: %q", buf.String())
	}
}

// rotationFailureCount scrapes the registry the way Prometheus does.
func rotationFailureCount(t *testing.T) float64 {
	t.Helper()
	rotationFailures()
	families, err := metrics.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != "qrdb_audit_rotation_failures_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			return m.GetCounter().GetValue()
		}
	}
	return 0
}

// TestChainFileLogger_Emit_WriteFailureDuringAFailedRotation: when BOTH fail,
// the write error is what Emit reports, because that is the one that means the
// record is not durable. The rotation failure rides its own counter.
//
// This replaces TestChainFileLogger_Emit_WriteErrorBeatsRotationError, whose
// premise -- that the two compete for the return value -- stopped being true
// when a rotation failure stopped producing one.
func TestChainFileLogger_Emit_WriteFailureDuringAFailedRotation(t *testing.T) {
	l := newTempChainLogger(t, WithMaxBytes(1))
	defer func() { _ = l.Close() }()

	rotErr := errors.New("rename refused")
	writeErr := errors.New("no space left on device")
	l.rotateFunc = func() error { return rotErr }
	l.writeFunc = func([]byte) (int, error) { return 0, writeErr }

	err := l.Emit(context.Background(), AuditEvent{Actor: "a"})
	if !errors.Is(err, writeErr) {
		t.Fatalf("expected the write error, got %v", err)
	}
	if errors.Is(err, rotErr) {
		t.Error("a rotation failure must not appear in Emit's answer at all")
	}
}

// TestChainFileLogger_Emit_RotationSuccessReportsNothing: a rotation that
// works is invisible to the caller.
func TestChainFileLogger_Emit_RotationSuccessReportsNothing(t *testing.T) {
	l := newTempChainLogger(t, WithMaxBytes(1))
	defer func() { _ = l.Close() }()

	if err := l.Emit(context.Background(), AuditEvent{Actor: "a", Action: "put"}); err != nil {
		t.Fatalf("a successful rotation must report nothing: %v", err)
	}
}

// TestChainFileLogger_Emit_MarshalFailure returns immediately: no line exists,
// so there is nothing the append could have salvaged.
func TestChainFileLogger_Emit_MarshalFailure(t *testing.T) {
	l := newTempChainLogger(t)
	defer func() { _ = l.Close() }()

	// A channel is not JSON-marshalable, so canonicalJSON fails first.
	err := l.Emit(context.Background(), AuditEvent{
		Actor:      "a",
		Attributes: map[string]any{"bad": make(chan int)},
	})
	var we *WriteError
	if !errors.As(err, &we) {
		t.Fatalf("expected *WriteError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// aggregateLogger: the fan-out must reach EVERY sink
// ---------------------------------------------------------------------------

// TestAggregateLogger_Emit_ContinuesPastFailure is the test the architecture
// doc calls the critical one. A stop-at-first-error fan-out passes a naive
// "the aggregate returned an error" assertion and loses the trail in every
// healthy destination behind the broken one.
func TestAggregateLogger_Emit_ContinuesPastFailure(t *testing.T) {
	broken := &failingSink{name: "broken", err: &WriteError{Backend: "broken", Err: errors.New("disk full")}}
	healthyRing := NewRingBuffer(8)
	healthyCounter := &failingSink{name: "healthy"} // err nil: succeeds, counts calls

	agg := NewAggregateLogger(broken, healthyRing, healthyCounter)

	err := agg.Emit(context.Background(), AuditEvent{Actor: "alice", Action: "put"})
	if err == nil {
		t.Fatal("the aggregate must report the broken sink's failure")
	}
	var we *WriteError
	if !errors.As(err, &we) {
		t.Fatalf("expected the child's *WriteError, got %T: %v", err, err)
	}
	if we.Backend != "broken" {
		t.Errorf("expected the first failing sink to be named, got %q", we.Backend)
	}

	if broken.calls != 1 {
		t.Errorf("broken sink attempted %d times, want 1", broken.calls)
	}
	if got := len(healthyRing.Snapshot()); got != 1 {
		t.Errorf("the ring buffer BEHIND the broken sink holds %d events, want 1 -- "+
			"a fan-out that stops at the first failure loses records to healthy sinks", got)
	}
	if healthyCounter.calls != 1 {
		t.Errorf("the last sink was reached %d times, want 1", healthyCounter.calls)
	}
}

// TestAggregateLogger_Emit_ReturnsFirstError: with two broken sinks the
// aggregate names the one that failed earliest in the configured order, so an
// operator reading the error sees a stable answer rather than whichever child
// happened to run last.
func TestAggregateLogger_Emit_ReturnsFirstError(t *testing.T) {
	first := &failingSink{err: &WriteError{Backend: "first", Err: errors.New("boom")}}
	second := &failingSink{err: &WriteError{Backend: "second", Err: errors.New("boom")}}

	err := NewAggregateLogger(first, second).Emit(context.Background(), AuditEvent{Actor: "a"})
	var we *WriteError
	if !errors.As(err, &we) {
		t.Fatalf("expected *WriteError, got %T: %v", err, err)
	}
	if we.Backend != "first" {
		t.Errorf("Backend = %q, want %q", we.Backend, "first")
	}
	if first.calls != 1 || second.calls != 1 {
		t.Errorf("both sinks must be attempted; got first=%d second=%d", first.calls, second.calls)
	}
}

func TestAggregateLogger_Emit_AllHealthy(t *testing.T) {
	ring := NewRingBuffer(4)
	agg := NewAggregateLogger(ring, NewNoopLogger())
	if err := agg.Emit(context.Background(), AuditEvent{Actor: "a"}); err != nil {
		t.Fatalf("all-healthy fan-out must report success: %v", err)
	}
	if got := len(ring.Snapshot()); got != 1 {
		t.Fatalf("ring holds %d events, want 1", got)
	}
}

// TestAggregateLogger_Emit_NoSinks: an aggregate over nothing recorded
// nothing and nothing failed. It must not refuse, or an embedder that
// constructs an empty aggregate would deny every request.
func TestAggregateLogger_Emit_NoSinks(t *testing.T) {
	if err := NewAggregateLogger().Emit(context.Background(), AuditEvent{Actor: "a"}); err != nil {
		t.Fatalf("empty aggregate must report success: %v", err)
	}
}

// ---------------------------------------------------------------------------
// decisionLogger: a pure adapter
// ---------------------------------------------------------------------------

func TestDecisionLogger_Log_PropagatesSinkError(t *testing.T) {
	sinkErr := &WriteError{Backend: "audit.ndjson", Err: errors.New("disk full")}
	l := NewDecisionLogger(&failingSink{err: sinkErr})

	err := l.Log(context.Background(), &Entry{Subject: "alice", Action: "auth.authorize"})
	if err == nil {
		t.Fatal("Log must propagate: discarding the sink's answer is exactly go-qrdb#335")
	}
	var we *WriteError
	if !errors.As(err, &we) {
		t.Fatalf("expected *WriteError, got %T: %v", err, err)
	}
	if we.Backend != "audit.ndjson" {
		t.Errorf("Backend = %q, want %q", we.Backend, "audit.ndjson")
	}
}

func TestDecisionLogger_Log_PropagatesSuccess(t *testing.T) {
	l := NewDecisionLogger(NewRingBuffer(4))
	if err := l.Log(context.Background(), &Entry{Subject: "alice"}); err != nil {
		t.Fatalf("a healthy sink must yield a nil Log: %v", err)
	}
}

// TestDecisionLogger_Log_NilEntryIsNotAFailure: there is nothing to record and
// nothing failed, so the middleware must not refuse the request.
func TestDecisionLogger_Log_NilEntryIsNotAFailure(t *testing.T) {
	l := NewDecisionLogger(&failingSink{err: errors.New("would have failed")})
	if err := l.Log(context.Background(), nil); err != nil {
		t.Fatalf("nil entry: %v", err)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// newTempChainLogger returns a ChainFileLogger over a file in t.TempDir().
func newTempChainLogger(t *testing.T, opts ...ChainFileOption) *ChainFileLogger {
	t.Helper()
	l, err := NewFileLogger(filepath.Join(t.TempDir(), "audit.ndjson"), opts...)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	return l
}

// ---------------------------------------------------------------------------
// EmitBestEffort: the other honest answer
// ---------------------------------------------------------------------------

// TestEmitBestEffort_RecordsOnAHealthySink is the ordinary path: the helper
// is a pass-through, not a discard.
func TestEmitBestEffort_RecordsOnAHealthySink(t *testing.T) {
	ring := NewRingBuffer(4)
	EmitBestEffort(context.Background(), ring, "user", AuditEvent{Actor: "a", Action: "create"})
	if got := len(ring.Snapshot()); got != 1 {
		t.Fatalf("ring holds %d events, want 1", got)
	}
}

// TestEmitBestEffort_LogsButDoesNotPanicOnFailure. The whole point is that a
// post-hoc emitter continues; the only observable is the operational log line,
// which must name the component so an operator can tell WHICH trail has a gap.
func TestEmitBestEffort_LogsButDoesNotPanicOnFailure(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	sink := &failingSink{err: &WriteError{Backend: "audit.ndjson", Err: errors.New("disk full")}}
	EmitBestEffort(context.Background(), sink, "custodian",
		AuditEvent{Actor: "a", Resource: "custodian:1", Action: "register", Outcome: OutcomeSuccess})

	if sink.calls != 1 {
		t.Fatalf("sink attempted %d times, want 1", sink.calls)
	}
	out := buf.String()
	for _, want := range []string{"audit: record not written", "component=custodian", "custodian:1", "disk full"} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q: %q", want, out)
		}
	}
}

// TestEmitBestEffort_NilSinkIsANoop: an embedder that wired no sink must not
// crash the operation whose record it is.
func TestEmitBestEffort_NilSinkIsANoop(t *testing.T) {
	EmitBestEffort(context.Background(), nil, "role", AuditEvent{Actor: "a"})
}

// TestEmitBestEffort_CountsTheGapByComponent. An slog.Error is not something
// anyone can alert on. This is the post-hoc counterpart to
// qrdb_auth_audit_write_failures_total, and the component label is what tells
// an operator WHICH trail is incomplete rather than merely that one is.
//
// Scraped from the registry rather than read off the counter, so the test
// proves the number is actually EXPOSED and not merely incremented.
func TestEmitBestEffort_CountsTheGapByComponent(t *testing.T) {
	sink := &failingSink{err: &WriteError{Backend: "audit.ndjson", Err: errors.New("disk full")}}

	before := bestEffortFailureCount(t, "custodian")
	otherBefore := bestEffortFailureCount(t, "ceremony")

	EmitBestEffort(context.Background(), sink, "custodian",
		AuditEvent{Actor: "a", Resource: "custodian:1", Action: "register"})
	EmitBestEffort(context.Background(), sink, "custodian",
		AuditEvent{Actor: "a", Resource: "custodian:2", Action: "register"})

	if after := bestEffortFailureCount(t, "custodian"); after != before+2 {
		t.Fatalf(`qrdb_audit_write_failures_total{component="custodian"} = %v, want %v`, after, before+2)
	}
	// The label must SEPARATE the subsystems, or it tells an operator nothing
	// a bare total would not.
	if otherAfter := bestEffortFailureCount(t, "ceremony"); otherAfter != otherBefore {
		t.Fatalf(`an unrelated component's counter moved: %v -> %v`, otherBefore, otherAfter)
	}
}

// TestEmitBestEffort_SuccessIsNotCounted: the counter must mean "this trail
// has a hole", not "a record was emitted".
func TestEmitBestEffort_SuccessIsNotCounted(t *testing.T) {
	before := bestEffortFailureCount(t, "user")
	EmitBestEffort(context.Background(), NewRingBuffer(4), "user", AuditEvent{Actor: "a"})
	if after := bestEffortFailureCount(t, "user"); after != before {
		t.Fatalf("a successful record moved the failure counter: %v -> %v", before, after)
	}
}

// bestEffortFailureCount scrapes the registry the way Prometheus does.
func bestEffortFailureCount(t *testing.T, component string) float64 {
	t.Helper()
	bestEffortWriteFailures() // ensure registration
	families, err := metrics.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != "qrdb_audit_write_failures_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "component" && l.GetValue() == component {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// The REAL rotate(), not the rotateFunc double
//
// rotateFunc replaces rotate() wholesale, so every test that uses it covers
// none of rotate's own failure modes. These drive the real one.
// ---------------------------------------------------------------------------

// TestChainFileLogger_Rotation_CloseFailureDoesNotWedgeTheNode is the
// regression test for the worst failure this sink had.
//
// os.File.Close releases the descriptor whether or not close(2) reported an
// error. rotate() returned early on a close error, which skipped the
// unconditional reopen AND the l.size reset -- so every later Emit re-entered
// tryRotate, wrote the anchor to a closed file, returned at the same line, and
// never reached openFile. Under the fail-closed middleware: 503 on every
// authorized request, permanently, not self-healing after the disk recovered,
// and the rotation backoff could not clear it because the retry path was
// itself what was broken.
//
// The trigger is exactly this feature's subject -- a deferred write error
// surfacing at close(2), ENOSPC/EDQUOT on NFS, EIO on delayed allocation --
// which is reproduced here by closing the handle out from under rotate so its
// own Close fails.
func TestChainFileLogger_Rotation_CloseFailureDoesNotWedgeTheNode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")
	l, err := NewFileLogger(path, WithMaxBytes(1))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer func() { _ = l.Close() }()

	// Close the descriptor behind the logger's back. rotate()'s own
	// l.f.Close() then fails with os.ErrClosed, which is the branch under test.
	// NO rotateFunc: this is the real rotate().
	l.mu.Lock()
	closeErr := l.f.Close()
	l.mu.Unlock()
	if closeErr != nil {
		t.Fatalf("precondition close: %v", closeErr)
	}

	ctx := context.Background()

	// The rotation fails. What must NOT happen is the sink dying.
	_ = l.Emit(ctx, AuditEvent{Actor: "a", Action: "during-the-failure"})

	// Every subsequent write must land. Before the fix these returned
	// *WriteError forever.
	const after = 5
	for i := 0; i < after; i++ {
		if emitErr := l.Emit(ctx, AuditEvent{Actor: "a", Action: "after"}); emitErr != nil {
			t.Fatalf("emit %d after a failed rotation close: %v -- the sink is wedged", i, emitErr)
		}
	}

	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read back: %v", readErr)
	}
	if got := strings.Count(string(data), `"action":"after"`); got != after {
		t.Fatalf("the trail holds %d of %d post-failure records", got, after)
	}
}

// TestChainFileLogger_Rotation_ReopenFailureRecoversOnTheNextEmit is the
// fourth failure mode, found by enumerating rotate() rather than by being
// shown it.
//
// When rotate()'s reopen fails -- a full disk refusing a new inode, a
// directory that became unwritable -- the logger was left with no usable file
// AND with l.size already reset to 0, so the size check never fired again and
// rotate() (openFile's only other caller) was never called again. One
// transient failure killed the sink until the process restarted: strictly
// worse than the close case, which at least kept retrying.
//
// ensureOpen restores the handle on the next Emit.
func TestChainFileLogger_Rotation_ReopenFailureRecoversOnTheNextEmit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")
	l, err := NewFileLogger(path, WithMaxBytes(1))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer func() { _ = l.Close() }()

	ctx := context.Background()

	// Simulate rotate() having lost the handle and failed to reopen.
	l.mu.Lock()
	_ = l.f.Close()
	l.f = nil
	l.size = 0
	l.mu.Unlock()

	if emitErr := l.Emit(ctx, AuditEvent{Actor: "a", Action: "recovered"}); emitErr != nil {
		t.Fatalf("the sink must reopen rather than stay dead: %v", emitErr)
	}

	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read back: %v", readErr)
	}
	if !strings.Contains(string(data), "recovered") {
		t.Fatalf("the record did not land: %q", string(data))
	}
}

// TestChainFileLogger_EnsureOpen_DoesNotResurrectAClosedLogger. Recovery must
// not become "Close() is advisory": a sink the owner closed stays closed, or
// the daemon keeps writing audit records through its own shutdown.
func TestChainFileLogger_EnsureOpen_DoesNotResurrectAClosedLogger(t *testing.T) {
	l := newTempChainLogger(t)
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	err := l.Emit(context.Background(), AuditEvent{Actor: "a"})
	if err == nil {
		t.Fatal("a closed logger must refuse, not reopen itself")
	}
	if !errors.Is(err, os.ErrClosed) {
		t.Errorf("want os.ErrClosed, got %v", err)
	}
	var we *WriteError
	if !errors.As(err, &we) {
		t.Errorf("expected *WriteError, got %T", err)
	}
}

// TestChainFileLogger_Rotation_RealRotateStillWorks is the control: with the
// real rotate() and no fault at all, rotation happens and both files are
// readable. Without it, a rotate() that always failed would pass the tests
// above.
func TestChainFileLogger_Rotation_RealRotateStillWorks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.ndjson")
	l, err := NewFileLogger(path, WithMaxBytes(300))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer func() { _ = l.Close() }()

	ctx := context.Background()
	for i := 0; i < 8; i++ {
		if emitErr := l.Emit(ctx, AuditEvent{Actor: "a", Action: "evt"}); emitErr != nil {
			t.Fatalf("emit %d: %v", i, emitErr)
		}
	}

	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("readdir: %v", readErr)
	}
	if len(entries) < 2 {
		t.Fatalf("expected at least one archive alongside the active file, got %d", len(entries))
	}
	// The anchor is what carries the cross-file handoff; a rotation that did
	// not write one has lost it.
	var anchors int
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(dir, e.Name()))
		anchors += strings.Count(string(data), "chain-anchor")
	}
	if anchors == 0 {
		t.Fatal("no chain-anchor entry was written by any rotation")
	}
}

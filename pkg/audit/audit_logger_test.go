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
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// errQueryLogger returns an error from Query.
type errQueryLogger struct{ err error }

func (e *errQueryLogger) Emit(_ context.Context, _ AuditEvent) error { return nil }
func (e *errQueryLogger) Query(_ context.Context, _ Filter) (*Page, error) {
	return nil, e.err
}
func (e *errQueryLogger) Close() error { return nil }

// errCloseLogger returns an error from Close.
type errCloseLogger struct{ err error }

func (e *errCloseLogger) Emit(_ context.Context, _ AuditEvent) error { return nil }
func (e *errCloseLogger) Query(_ context.Context, _ Filter) (*Page, error) {
	return &Page{Total: -1}, nil
}
func (e *errCloseLogger) Close() error { return e.err }

// trackCloseLogger records whether Close was called.
type trackCloseLogger struct{ onClose func() }

func (t *trackCloseLogger) Emit(_ context.Context, _ AuditEvent) error { return nil }
func (t *trackCloseLogger) Query(_ context.Context, _ Filter) (*Page, error) {
	return &Page{Total: -1}, nil
}
func (t *trackCloseLogger) Close() error { t.onClose(); return nil }

// ---------------------------------------------------------------------------
// aggregateLogger tests
// ---------------------------------------------------------------------------

func TestAggregateLogger_Emit_FansOut(t *testing.T) {
	r1 := NewRingBuffer(100)
	r2 := NewRingBuffer(100)
	agg := NewAggregateLogger(r1, r2)
	ctx := context.Background()
	agg.Emit(ctx, testEvent("alice", "barrier", "unseal"))

	for i, rb := range []*RingBuffer{r1, r2} {
		page, err := rb.Query(ctx, Filter{Limit: 10})
		if err != nil {
			t.Fatalf("r%d Query: %v", i+1, err)
		}
		if len(page.Events) != 1 {
			t.Fatalf("r%d: expected 1 event, got %d", i+1, len(page.Events))
		}
	}
}

func TestAggregateLogger_Query_ReturnsFirstNonEmpty(t *testing.T) {
	empty := NewNoopLogger()
	r := NewRingBuffer(100)
	ctx := context.Background()
	r.Emit(ctx, testEvent("bob", "kv", "put"))

	agg := NewAggregateLogger(empty, r)
	page, err := agg.Query(ctx, Filter{Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("expected 1 event from ring, got %d", len(page.Events))
	}
}

func TestAggregateLogger_Close_ClosesAll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.ndjson")
	fl, err := NewFileLogger(path)
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	r := NewRingBuffer(10)
	agg := NewAggregateLogger(fl, r)
	if err := agg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestAggregateLogger_Empty(t *testing.T) {
	agg := NewAggregateLogger()
	ctx := context.Background()
	agg.Emit(ctx, testEvent("ghost", "kv", "get"))
	page, err := agg.Query(ctx, Filter{Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 0 {
		t.Fatalf("expected 0 events from empty aggregate, got %d", len(page.Events))
	}
}

func TestAggregateLogger_Query_PropagatesError(t *testing.T) {
	errLogger := &errQueryLogger{err: errors.New("query failed")}
	agg := NewAggregateLogger(errLogger)
	ctx := context.Background()
	_, err := agg.Query(ctx, Filter{Limit: 10})
	if err == nil {
		t.Fatal("expected error from aggregateLogger.Query, got nil")
	}
}

func TestAggregateLogger_Close_PropagatesError(t *testing.T) {
	errLogger := &errCloseLogger{err: errors.New("close failed")}
	agg := NewAggregateLogger(errLogger)
	if err := agg.Close(); err == nil {
		t.Fatal("expected error from aggregateLogger.Close, got nil")
	}
}

func TestAggregateLogger_Close_ContinuesAfterError(t *testing.T) {
	// Even if one child Close fails, remaining children should be closed.
	closed := false
	cl := &trackCloseLogger{onClose: func() { closed = true }}
	errLogger := &errCloseLogger{err: errors.New("close failed")}
	// errLogger is first; cl should still be closed.
	agg := NewAggregateLogger(errLogger, cl)
	_ = agg.Close()
	if !closed {
		t.Fatal("second logger should be closed even after first close error")
	}
}

// ---------------------------------------------------------------------------
// NoopLogger tests
// ---------------------------------------------------------------------------

func TestNoopLogger_EmitAndQuery(t *testing.T) {
	n := NewNoopLogger()
	ctx := context.Background()
	n.Emit(ctx, testEvent("nobody", "kv", "put"))
	page, err := n.Query(ctx, Filter{Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(page.Events))
	}
	if page.Total != -1 {
		t.Fatalf("expected Total -1, got %d", page.Total)
	}
}

func TestNoopLogger_Close_Exported(t *testing.T) {
	if err := NewNoopLogger().Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// SlogSink tests
// ---------------------------------------------------------------------------

func TestSlogSink_Emit_StructuredFields(t *testing.T) {
	var buf strings.Builder
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(handler)
	sink := NewSlogSinkWith(logger)

	ctx := context.Background()
	sink.Emit(ctx, testEvent("slogger", "ceremony", "enroll"))

	output := buf.String()
	if !strings.Contains(output, "audit") {
		t.Errorf("expected 'audit' in slog output, got: %s", output)
	}
	if !strings.Contains(output, "slogger") {
		t.Errorf("expected actor 'slogger' in slog output, got: %s", output)
	}
	if !strings.Contains(output, "enroll") {
		t.Errorf("expected action 'enroll' in slog output, got: %s", output)
	}
}

func TestSlogSink_Query_ReturnsEmpty(t *testing.T) {
	sink := NewSlogSink()
	ctx := context.Background()
	page, err := sink.Query(ctx, Filter{Limit: 10})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 0 {
		t.Fatalf("expected 0 events from slog sink, got %d", len(page.Events))
	}
}

func TestSlogSink_Close(t *testing.T) {
	if err := NewSlogSink().Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestSlogSink_Emit_ZeroTimestamp(t *testing.T) {
	// Emit with zero timestamp should fill in current time without panicking.
	var buf strings.Builder
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	sink := NewSlogSinkWith(slog.New(handler))
	ev := AuditEvent{Actor: "agent", Resource: "kv", Action: "get", Outcome: OutcomeSuccess}
	sink.Emit(context.Background(), ev)
	if !strings.Contains(buf.String(), "agent") {
		t.Fatalf("expected actor in output: %s", buf.String())
	}
}

// ---------------------------------------------------------------------------
// AuditEvent tests
// ---------------------------------------------------------------------------

func TestAuditEvent_OutcomeConstants(t *testing.T) {
	if OutcomeSuccess != "success" {
		t.Fatalf("OutcomeSuccess = %q, want %q", OutcomeSuccess, "success")
	}
	if OutcomeFailure != "failure" {
		t.Fatalf("OutcomeFailure = %q, want %q", OutcomeFailure, "failure")
	}
	if OutcomeDenied != "denied" {
		t.Fatalf("OutcomeDenied = %q, want %q", OutcomeDenied, "denied")
	}
	// The gating record's word is the gate's own (gateOutcomePermitted).
	if OutcomePermitted != gateOutcomePermitted {
		t.Fatalf("OutcomePermitted = %q, want %q", OutcomePermitted, gateOutcomePermitted)
	}
}

func TestNormalizeLimit_Defaults(t *testing.T) {
	tests := []struct {
		in   int
		want int
	}{
		{0, defaultLimit},
		{-5, defaultLimit},
		{50, 50},
		{1000, 1000},
		{2000, maxLimit},
	}
	for _, tt := range tests {
		got := normalizeLimit(tt.in)
		if got != tt.want {
			t.Errorf("normalizeLimit(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestErrChainBroken_Error(t *testing.T) {
	err := &ChainBrokenError{Index: 3, Expected: "abc", Got: "def"}
	msg := err.Error()
	if !strings.Contains(msg, "3") || !strings.Contains(msg, "abc") || !strings.Contains(msg, "def") {
		t.Fatalf("unexpected error message: %s", msg)
	}
}

func TestErrChainBroken_Error_Zero(t *testing.T) {
	// Index 0 edge case for the itoa helper.
	err := &ChainBrokenError{Index: 0, Expected: "0000", Got: "ffff"}
	msg := err.Error()
	if !strings.Contains(msg, "0") {
		t.Fatalf("unexpected error message: %s", msg)
	}
}

func TestErrChainBroken_Error_Negative(t *testing.T) {
	// Negative index covers the neg branch in itoa.
	err := &ChainBrokenError{Index: -1, Expected: "x", Got: "y"}
	msg := err.Error()
	if !strings.Contains(msg, "-1") {
		t.Fatalf("unexpected error message: %s", msg)
	}
}

func TestRetentionError_Error(t *testing.T) {
	inner := errors.New("disk full")
	re := &RetentionError{Err: inner}
	msg := re.Error()
	if !strings.Contains(msg, "retention") {
		t.Fatalf("unexpected error message: %s", msg)
	}
	if !strings.Contains(msg, "disk full") {
		t.Fatalf("expected inner error in message: %s", msg)
	}
}

func TestRetentionError_Unwrap(t *testing.T) {
	inner := &FileError{Path: "/x", Err: nil}
	re := &RetentionError{Err: inner}
	if re.Unwrap() != inner {
		t.Fatal("Unwrap should return inner error")
	}
}

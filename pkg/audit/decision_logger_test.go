// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package audit

import (
	"context"
	"testing"
	"time"
)

// recordingAuditLogger captures emitted events for assertions.
type recordingAuditLogger struct {
	events []AuditEvent
}

func (r *recordingAuditLogger) Emit(_ context.Context, ev AuditEvent) {
	r.events = append(r.events, ev)
}
func (r *recordingAuditLogger) Query(context.Context, Filter) (*Page, error) {
	return &Page{}, nil
}
func (r *recordingAuditLogger) Close() error { return nil }

// TestDecisionLogger_ForwardsEntryAsAuditEvent proves a middleware decision
// entry lands in the canonical event log with every field mapped, so
// authorization decisions share the same tamper-evident chain as all other
// audited operations.
func TestDecisionLogger_ForwardsEntryAsAuditEvent(t *testing.T) {
	sink := &recordingAuditLogger{}
	l := NewDecisionLogger(sink)

	ts := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	err := l.Log(context.Background(), &Entry{
		Timestamp: ts,
		TenantID:  "tenant-1",
		Subject:   "alice",
		Action:    "auth.authorize",
		Resource:  "keys:sign",
		Result:    ResultDenied,
		Metadata:  map[string]string{"transport": "rest"},
	})
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Actor != "alice" || ev.TenantID != "tenant-1" ||
		ev.Action != "auth.authorize" || ev.Resource != "keys:sign" ||
		ev.Outcome != ResultDenied || !ev.Timestamp.Equal(ts) {
		t.Fatalf("mismapped event: %+v", ev)
	}
	if got := ev.Attributes["transport"]; got != "rest" {
		t.Fatalf("attributes: got %v", ev.Attributes)
	}
}

// TestDecisionLogger_NilEntryAndClose proves the error-handling contract: a
// nil entry is ignored without emitting, and Close never touches the sink's
// lifecycle (the sink's owner closes it).
func TestDecisionLogger_NilEntryAndClose(t *testing.T) {
	sink := &recordingAuditLogger{}
	l := NewDecisionLogger(sink)
	if err := l.Log(context.Background(), nil); err != nil {
		t.Fatalf("Log(nil): %v", err)
	}
	if len(sink.events) != 0 {
		t.Fatalf("nil entry must not emit; got %d", len(sink.events))
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("NewDecisionLogger(nil) must panic: a nil sink is a programmer error")
		}
	}()
	NewDecisionLogger(nil)
}

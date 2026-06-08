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

package sdk

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
)

// TestNewAuditRingBuffer_EmitQueryRoundTrip verifies the SDK ring-buffer
// re-export returns a working in-memory NodeAuditLogger whose emitted events
// are retrievable through Query.
func TestNewAuditRingBuffer_EmitQueryRoundTrip(t *testing.T) {
	logger := NewAuditRingBuffer(16)
	if logger == nil {
		t.Fatal("NewAuditRingBuffer returned nil")
	}

	ctx := context.Background()
	logger.Emit(ctx, audit.AuditEvent{
		Timestamp: time.Now().UTC(),
		Actor:     "alice",
		Resource:  "barrier",
		Action:    "unseal",
		Outcome:   audit.OutcomeSuccess,
	})

	page, err := logger.Query(ctx, audit.Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(page.Events))
	}
	if got := page.Events[0].Actor; got != "alice" {
		t.Fatalf("actor = %q, want alice", got)
	}
}

// TestNewAuditFileLogger_EmitQueryRoundTrip verifies the SDK file-logger
// re-export returns a working HMAC-chained NodeAuditLogger backed by a file.
func TestNewAuditFileLogger_EmitQueryRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")

	logger, err := NewAuditFileLogger(path)
	if err != nil {
		t.Fatalf("NewAuditFileLogger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	ctx := context.Background()
	logger.Emit(ctx, audit.AuditEvent{
		Timestamp: time.Now().UTC(),
		Actor:     "bob",
		Resource:  "keys",
		Action:    "sign",
		Outcome:   audit.OutcomeSuccess,
	})

	page, err := logger.Query(ctx, audit.Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(page.Events))
	}
	if got := page.Events[0].Actor; got != "bob" {
		t.Fatalf("actor = %q, want bob", got)
	}
}

// TestWithAuditLogger_ReturnsServerOption verifies the SDK re-export produces
// a non-nil ServerOption usable at node construction.
func TestWithAuditLogger_ReturnsServerOption(t *testing.T) {
	opt := WithAuditLogger(NewAuditRingBuffer(8))
	if opt == nil {
		t.Fatal("WithAuditLogger returned nil ServerOption")
	}
}

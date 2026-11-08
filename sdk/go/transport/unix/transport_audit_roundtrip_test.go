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

package unix

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// queryAuditLogger is an audit.AuditLogger that records the filter the
// server hands it and answers with a fixed page, so a test sees both what
// the client put on the wire and what it made of the reply.
type queryAuditLogger struct {
	mu      sync.Mutex
	queries []audit.Filter
	page    *audit.Page
	err     error
}

var _ audit.AuditLogger = (*queryAuditLogger)(nil)

func (l *queryAuditLogger) Emit(context.Context, audit.AuditEvent) error { return nil }

func (l *queryAuditLogger) Query(_ context.Context, f audit.Filter) (*audit.Page, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.queries = append(l.queries, f)
	if l.err != nil {
		return nil, l.err
	}
	return l.page, nil
}

func (l *queryAuditLogger) Close() error { return nil }

func (l *queryAuditLogger) lastQuery(t *testing.T) audit.Filter {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.queries) == 0 {
		t.Fatal("the audit logger was never queried")
	}
	return l.queries[len(l.queries)-1]
}

func TestUnixRT_QueryAudit_ForwardsTheFilterAndConvertsThePage(t *testing.T) {
	at := time.Date(2026, 4, 2, 9, 30, 15, 0, time.UTC)
	logger := &queryAuditLogger{page: &audit.Page{
		Events: []audit.AuditEvent{
			{
				Timestamp:  at,
				Actor:      "alice",
				TenantID:   "acme",
				Role:       "officer",
				Resource:   "barrier",
				Action:     "unseal",
				Outcome:    "success",
				Attributes: map[string]any{"share_index": "3"},
				ClientIP:   "192.0.2.7",
				RequestID:  "req-1",
				PrevHash:   "ab12",
			},
			{Timestamp: at.Add(time.Second), Actor: "bob", Resource: "kv", Action: "put", Outcome: "denied:policy"},
		},
		Next:  "cursor-2",
		Total: 7,
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithAuditLogger(logger))
	defer cleanup()

	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.FixedZone("x", 3600))
	to := from.Add(48 * time.Hour)
	page, err := tr.QueryAudit(context.Background(), transport.AuditFilter{
		From:     from,
		To:       to,
		Actor:    "alice",
		Resource: "barrier",
		Action:   "unseal",
		Outcome:  "success",
		Limit:    25,
		Cursor:   "cursor-1",
	})
	if err != nil {
		t.Fatalf("QueryAudit: %v", err)
	}

	got := logger.lastQuery(t)
	if got.Actor != "alice" || got.Resource != "barrier" || got.Action != "unseal" ||
		got.Outcome != "success" || got.Limit != 25 || got.Cursor != "cursor-1" {
		t.Errorf("server filter = %+v, want every field forwarded", got)
	}
	if !got.From.Equal(from) || !got.To.Equal(to) {
		t.Errorf("server range = [%v, %v), want [%v, %v)", got.From, got.To, from, to)
	}
	// An unauthenticated local caller is the system tenant, so the server
	// widens the query to untenanted (platform) events.
	if !got.Untenanted {
		t.Errorf("server filter Untenanted = false, want true for the system tenant")
	}

	want := []transport.AuditEvent{
		{
			Timestamp:  at,
			Actor:      "alice",
			TenantID:   "acme",
			Role:       "officer",
			Resource:   "barrier",
			Action:     "unseal",
			Outcome:    "success",
			Attributes: map[string]any{"share_index": "3"},
			ClientIP:   "192.0.2.7",
			RequestID:  "req-1",
			PrevHash:   "ab12",
		},
		{Timestamp: at.Add(time.Second), Actor: "bob", Resource: "kv", Action: "put", Outcome: "denied:policy"},
	}
	if !reflect.DeepEqual(page.Events, want) {
		t.Errorf("events = %+v\nwant %+v", page.Events, want)
	}
	if page.Next != "cursor-2" || page.Total != 7 {
		t.Errorf("page Next/Total = %q/%d, want cursor-2/7", page.Next, page.Total)
	}
}

func TestUnixRT_QueryAudit_EmptyRangeIsNotSent(t *testing.T) {
	logger := &queryAuditLogger{page: &audit.Page{}}
	tr, cleanup := startAuthServer(t, unixserver.WithAuditLogger(logger))
	defer cleanup()

	page, err := tr.QueryAudit(context.Background(), transport.AuditFilter{})
	if err != nil {
		t.Fatalf("QueryAudit: %v", err)
	}
	if got := logger.lastQuery(t); !got.From.IsZero() || !got.To.IsZero() {
		t.Errorf("server range = [%v, %v), want both unbounded", got.From, got.To)
	}
	if len(page.Events) != 0 || page.Next != "" || page.Total != 0 {
		t.Errorf("page = %+v, want empty", page)
	}
}

func TestUnixRT_QueryAudit_Refusals(t *testing.T) {
	logger := &queryAuditLogger{err: errors.New("disk gone")}
	tr, cleanup := startAuthServer(t, unixserver.WithAuditLogger(logger))
	defer cleanup()
	ctx := context.Background()

	if _, err := tr.QueryAudit(ctx, transport.AuditFilter{}); err == nil {
		t.Fatal("QueryAudit with a failing logger: err = nil")
	} else {
		requireUnixRequestError(t, err, "QueryAudit")
	}

	// A caller may not read another tenant's events; the refusal comes back
	// before the logger is consulted.
	before := len(logger.queries)
	if _, err := tr.QueryAudit(ctx, transport.AuditFilter{TenantID: "other-tenant"}); err == nil {
		t.Fatal("cross-tenant QueryAudit: err = nil")
	} else {
		requireUnixRequestError(t, err, "QueryAudit")
	}
	if len(logger.queries) != before {
		t.Error("cross-tenant QueryAudit reached the logger")
	}

	// No replicated audit is configured on this server.
	var ni *transport.NotImplementedError
	if _, err := tr.QueryAudit(ctx, transport.AuditFilter{Replicated: true}); !errors.As(err, &ni) || ni.Method != "QueryAudit" {
		t.Fatalf("replicated QueryAudit: err = %v, want NotImplementedError{QueryAudit}", err)
	}
	if _, err := tr.VerifyReplicatedChain(ctx, "acme"); !errors.As(err, &ni) || ni.Method != "VerifyReplicatedChain" {
		t.Fatalf("VerifyReplicatedChain: err = %v, want NotImplementedError{VerifyReplicatedChain}", err)
	}
}

func TestUnixRT_QueryAudit_WithoutAnAuditLoggerIsNotImplemented(t *testing.T) {
	tr, cleanup := startAuthServer(t)
	defer cleanup()

	var ni *transport.NotImplementedError
	if _, err := tr.QueryAudit(context.Background(), transport.AuditFilter{}); !errors.As(err, &ni) || ni.Method != "QueryAudit" {
		t.Fatalf("QueryAudit: err = %v, want NotImplementedError{QueryAudit}", err)
	}
}

func TestUnixRT_EmitAudit_IsNotImplementedAndAuditClientIsTheTransport(t *testing.T) {
	tr, cleanup := startAuthServer(t)
	defer cleanup()

	if tr.AuditClient() != transport.AuditClient(tr) {
		t.Fatal("AuditClient did not return the transport itself")
	}
	var ni *transport.NotImplementedError
	if err := tr.EmitAudit(context.Background(), transport.AuditEvent{Actor: "alice"}); !errors.As(err, &ni) || ni.Method != "EmitAudit" {
		t.Fatalf("EmitAudit: err = %v, want NotImplementedError{EmitAudit}", err)
	}
}

func TestUnixRT_Audit_Closed(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithAuditLogger(&queryAuditLogger{page: &audit.Page{}}))
	cleanup()
	ctx := context.Background()

	_, err := tr.QueryAudit(ctx, transport.AuditFilter{})
	assertClosed(t, err)
	assertClosed(t, tr.EmitAudit(ctx, transport.AuditEvent{}))
	_, err = tr.VerifyReplicatedChain(ctx, "acme")
	assertClosed(t, err)
}

func TestParseAuditTimestamp_EmptyAndMalformedAreZero(t *testing.T) {
	if got := parseAuditTimestamp(""); !got.IsZero() {
		t.Errorf("parseAuditTimestamp(\"\") = %v, want zero", got)
	}
	if got := parseAuditTimestamp("yesterday"); !got.IsZero() {
		t.Errorf("parseAuditTimestamp(malformed) = %v, want zero", got)
	}
	want := time.Date(2026, 4, 2, 9, 30, 15, 0, time.UTC)
	if got := parseAuditTimestamp("2026-04-02T09:30:15Z"); !got.Equal(want) {
		t.Errorf("parseAuditTimestamp = %v, want %v", got, want)
	}
}

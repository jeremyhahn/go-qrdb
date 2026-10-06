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

package trustanchor

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

func findAnchorEvent(evs []audit.AuditEvent, action string) *audit.AuditEvent {
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Action == action {
			return &evs[i]
		}
	}
	return nil
}

// TestKVRegistry_Add_EmitsTrustAnchorAdd pins the owner ruling: Add emits
// trust_anchor.add on the compliance trail, keyed by the AFFECTED tenant.
func TestKVRegistry_Add_EmitsTrustAnchorAdd(t *testing.T) {
	ctx := context.Background()
	ring := audit.NewRingBuffer(16)
	r := NewKVRegistry(kvstore.NewMemoryKVStore(), WithAnchorAuditLogger(ring))
	root := newRoot(t, "r")

	if _, err := r.Add(ctx, "acme", EncodePEM(root.cert)); err != nil {
		t.Fatal(err)
	}

	ev := findAnchorEvent(ring.Snapshot(), ActionTrustAnchorAdd)
	if ev == nil {
		t.Fatal("no trust_anchor.add event recorded")
	}
	if ev.TenantID != "acme" || ev.Outcome != audit.OutcomeSuccess {
		t.Fatalf("trust_anchor.add event = %+v, want tenant acme, outcome success", ev)
	}
}

// TestKVRegistry_Remove_EmitsTrustAnchorRemove mirrors the Add test for
// withdrawal.
func TestKVRegistry_Remove_EmitsTrustAnchorRemove(t *testing.T) {
	ctx := context.Background()
	ring := audit.NewRingBuffer(16)
	r := NewKVRegistry(kvstore.NewMemoryKVStore(), WithAnchorAuditLogger(ring))
	root := newRoot(t, "r")
	if _, err := r.Add(ctx, "acme", EncodePEM(root.cert)); err != nil {
		t.Fatal(err)
	}

	if err := r.Remove(ctx, "acme", Fingerprint(root.cert)); err != nil {
		t.Fatal(err)
	}

	ev := findAnchorEvent(ring.Snapshot(), ActionTrustAnchorRemove)
	if ev == nil {
		t.Fatal("no trust_anchor.remove event recorded")
	}
	if ev.TenantID != "acme" || ev.Outcome != audit.OutcomeSuccess {
		t.Fatalf("trust_anchor.remove event = %+v, want tenant acme, outcome success", ev)
	}
}

// refusingAnchorLogger is an audit.AuditLogger whose Emit always fails.
type refusingAnchorLogger struct{}

func (refusingAnchorLogger) Emit(context.Context, audit.AuditEvent) error {
	return errors.New("audit sink unavailable")
}
func (refusingAnchorLogger) Query(context.Context, audit.Filter) (*audit.Page, error) {
	return nil, errors.New("audit sink unavailable")
}
func (refusingAnchorLogger) Close() error { return nil }

var _ audit.AuditLogger = refusingAnchorLogger{}

// TestKVRegistry_Add_FailClosedOnAuditRefusal pins the audit failure
// policy: under the default (fail-closed), an Add whose GATING record
// cannot be written is refused and nothing is registered.
func TestKVRegistry_Add_FailClosedOnAuditRefusal(t *testing.T) {
	ctx := context.Background()
	r := NewKVRegistry(kvstore.NewMemoryKVStore(), WithAnchorAuditLogger(refusingAnchorLogger{}))
	root := newRoot(t, "r")

	_, err := r.Add(ctx, "acme", EncodePEM(root.cert))
	var gateErr *audit.GateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("Add over a refusing trail = %v, want *audit.GateError", err)
	}
	if list, _ := r.List(ctx, "acme"); len(list) != 0 {
		t.Fatalf("a gate-refused Add registered %d anchors", len(list))
	}
}

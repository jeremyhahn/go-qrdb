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

package seal

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// eventsOfAction returns ring's events of action.
func eventsOfAction(ring *audit.RingBuffer, action string) []audit.AuditEvent {
	var out []audit.AuditEvent
	for _, ev := range ring.Snapshot() {
		if ev.Action == action {
			out = append(out, ev)
		}
	}
	return out
}

// The adapter maps a barrier event onto the compliance trail: keyed by the
// owning tenant, naming the barrier, outcomes in the trail's vocabulary, and
// only allow-listed details.
func TestComplianceAuditLogger_MapsBarrierEvents(t *testing.T) {
	ring := audit.NewRingBuffer(16)
	l := NewComplianceAuditLogger(ring, "acme", TenantBarrierAuditName("acme"))
	ctx := context.Background()
	for _, ev := range []*AuditEvent{
		{Subject: "seal", Action: "unseal", Outcome: "allow", Details: map[string]string{"strategy": "passphrase", "secret": "s3cr3t"}},
		{Subject: "seal", Action: "unseal", Outcome: "deny", Details: map[string]string{"error": "kms: key arn:secret-CANARY denied", "strategy": "passphrase"}},
		{Subject: "seal", Action: "holder-record-conflict", Outcome: "deny", Details: map[string]string{"reason": "conflict", "username": "bob"}},
		{Subject: "seal", Action: "seal", Outcome: "deny"},
		nil,
	} {
		if err := l.Log(ctx, ev); err != nil {
			t.Fatalf("Log = %v", err)
		}
	}
	evs := ring.Snapshot()
	if len(evs) != 4 {
		t.Fatalf("recorded %d events, want 4", len(evs))
	}
	for _, ev := range evs {
		if ev.TenantID != "acme" || ev.Resource != BarrierAuditResource || ev.Attributes["barrier"] != "tenant:acme" || ev.Actor != "seal" {
			t.Fatalf("event = %+v", ev)
		}
		for k := range ev.Attributes {
			if k != "barrier" && !barrierAuditAttributes[k] {
				t.Fatalf("attribute %q is not on the allow-list", k)
			}
		}
	}
	if _, ok := evs[0].Attributes["secret"]; ok {
		t.Fatal("a detail off the allow-list reached the trail")
	}
	raw, err := json.Marshal(evs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "CANARY") || strings.Contains(string(raw), "s3cr3t") {
		t.Fatalf("an error's or a detail's text reached the trail: %s", raw)
	}
	for i, want := range []string{audit.OutcomeSuccess, audit.OutcomeFailure, audit.OutcomeDenied, audit.OutcomeDenied} {
		if evs[i].Outcome != want {
			t.Fatalf("event %d outcome = %q, want %q", i, evs[i].Outcome, want)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if err := ring.Emit(ctx, audit.AuditEvent{Action: "after-close"}); err != nil {
		t.Fatalf("the adapter closed the composer's trail: %v", err)
	}
}

// A nil trail yields no logger, so the barrier records nothing; an empty
// tenant is the system tenant.
func TestNewComplianceAuditLogger_NilTrailAndSystemTenant(t *testing.T) {
	if l := NewComplianceAuditLogger(nil, "acme", "tenant:acme"); l != nil {
		t.Fatalf("a nil trail = %T, want nil", l)
	}
	ring := audit.NewRingBuffer(4)
	_ = NewComplianceAuditLogger(ring, "", SystemBarrierAuditName).Log(context.Background(), &AuditEvent{Action: "seal", Outcome: "allow"})
	if got := ring.Snapshot()[0].TenantID; got != tenantid.System {
		t.Fatalf("tenant = %q, want the system tenant", got)
	}
	if got := DatabaseBarrierAuditName("", "vault"); got != "database:system/vault" {
		t.Fatalf("DatabaseBarrierAuditName = %q", got)
	}
}

// refusingTrail refuses every record, as a full disk refuses the chained file.
type refusingTrail struct{ calls int }

func (r *refusingTrail) Emit(context.Context, audit.AuditEvent) error {
	r.calls++
	return errors.New("disk full")
}
func (r *refusingTrail) Query(context.Context, audit.Filter) (*audit.Page, error) {
	return &audit.Page{}, nil
}
func (r *refusingTrail) Close() error { return nil }

// A barrier's record is post-hoc: a trail that refuses it leaves the
// barrier operation's outcome as it was.
func TestComplianceAuditLogger_RefusedRecordDoesNotFailTheBarrier(t *testing.T) {
	trail := &refusingTrail{}
	strategy, err := NewSoftwareStrategy(context.Background(), []byte("compliance-passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), BarrierConfig{
		RootKeyPath: "root-key",
		AuditLogger: NewComplianceAuditLogger(trail, tenantid.System, SystemBarrierAuditName),
	}, strategy)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Initialize(context.Background(), MustCredentials(context.Background(), []byte("compliance-passphrase"))); err != nil {
		t.Fatalf("Initialize with a refusing trail = %v", err)
	}
	if trail.calls == 0 {
		t.Fatal("the record was not attempted")
	}
}

// A registry with an audit trail records every tenant barrier's events on
// it, keyed by the tenant; a registration that brings its own logger keeps
// it.
func TestBarrierRegistry_WithAuditTrail(t *testing.T) {
	ctx := context.Background()
	ring := audit.NewRingBuffer(32)
	system, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), BarrierConfig{RootKeyPath: "root-key"}, NewNoneStrategy())
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Initialize(ctx, Credentials{}); err != nil {
		t.Fatal(err)
	}
	reg, err := NewBarrierRegistry(testLogger(), system, WithAuditTrail(ring), WithAuditTrail(nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.RegisterTenant(ctx, "acme", NewMemoryBackend(), BarrierConfig{}, NewNoneStrategy()); err != nil {
		t.Fatal(err)
	}
	if err := reg.InitializeTenant(ctx, "acme", Credentials{}); err != nil {
		t.Fatal(err)
	}
	inits := eventsOfAction(ring, "initialize")
	// A tenant barrier's events are platform events: the system tenant's
	// trail, naming the tenant's barrier.
	if len(inits) != 1 || inits[0].TenantID != tenantid.System || inits[0].Attributes["barrier"] != "tenant:acme" || inits[0].Outcome != audit.OutcomeSuccess {
		t.Fatalf("initialize records = %+v", inits)
	}

	own := audit.NewRingBuffer(8)
	if _, err := reg.RegisterTenant(ctx, "globex", NewMemoryBackend(), BarrierConfig{
		AuditLogger: NewComplianceAuditLogger(own, "globex", "custom"),
	}, NewNoneStrategy()); err != nil {
		t.Fatal(err)
	}
	if err := reg.InitializeTenant(ctx, "globex", Credentials{}); err != nil {
		t.Fatal(err)
	}
	if len(eventsOfAction(own, "initialize")) != 1 {
		t.Fatal("a registration's own logger was replaced")
	}
	if len(eventsOfAction(ring, "initialize")) != 1 {
		t.Fatal("a registration with its own logger also recorded on the registry trail")
	}
}

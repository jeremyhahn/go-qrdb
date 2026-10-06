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

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
)

// findEvent returns the last event in evs with the given action, or nil.
func findEvent(evs []audit.AuditEvent, action string) *audit.AuditEvent {
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Action == action {
			return &evs[i]
		}
	}
	return nil
}

// TestCertEnrollmentService_SignCertificate_EmitsCertIssue pins the owner
// ruling: SignCertificate emits cert.issue on the compliance trail, keyed
// by the AFFECTED tenant (the request's own, never the caller's unless they
// are the same).
func TestCertEnrollmentService_SignCertificate_EmitsCertIssue(t *testing.T) {
	ctx := context.Background()
	users := newCertEnrollmentUserStore(t)
	provider := newCertEnrollmentProvider(t)
	ring := audit.NewRingBuffer(16)
	svc := NewCertEnrollmentService(provider, users, nil, WithEnrollmentAuditLogger(ring))

	createTestAccount(t, users, "acme", "alice")
	_, csrPEM, err := certtest.NewCSR("alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SignCertificate(ctx, &SignCertificateRequest{
		TenantID: "acme", Username: "alice", CSRPEM: csrPEM, TTL: time.Hour,
	}); err != nil {
		t.Fatalf("SignCertificate: %v", err)
	}

	evs := ring.Snapshot()
	ev := findEvent(evs, ActionCertIssue)
	if ev == nil {
		t.Fatalf("no %s event recorded; events = %+v", ActionCertIssue, evs)
	}
	if ev.TenantID != "acme" {
		t.Fatalf("cert.issue TenantID = %q, want acme", ev.TenantID)
	}
	if ev.Outcome != audit.OutcomeSuccess {
		t.Fatalf("cert.issue Outcome = %q, want success", ev.Outcome)
	}
}

// TestCertEnrollmentService_EnrollCertificate_EmitsCertEnroll mirrors the
// issue test for the bring-your-own path.
func TestCertEnrollmentService_EnrollCertificate_EmitsCertEnroll(t *testing.T) {
	ctx := context.Background()
	users := newCertEnrollmentUserStore(t)
	createTestAccount(t, users, "acme", "alice")
	registry := trustanchor.NewKVRegistry(kvstore.NewMemoryKVStore())
	root := newTestRoot(t, "acme-root")
	if _, err := registry.Add(ctx, "acme", pemOf(root.cert)); err != nil {
		t.Fatal(err)
	}
	leaf := root.clientLeaf(t, "alice", "acme")
	ring := audit.NewRingBuffer(16)
	svc := NewCertEnrollmentService(nil, users, registry, WithEnrollmentAuditLogger(ring))

	if _, err := svc.EnrollCertificate(ctx, &EnrollCertificateRequest{TenantID: "acme", Username: "alice", CertPEM: pemOf(leaf)}); err != nil {
		t.Fatalf("EnrollCertificate: %v", err)
	}

	evs := ring.Snapshot()
	ev := findEvent(evs, ActionCertEnroll)
	if ev == nil {
		t.Fatalf("no %s event recorded; events = %+v", ActionCertEnroll, evs)
	}
	if ev.TenantID != "acme" || ev.Outcome != audit.OutcomeSuccess {
		t.Fatalf("cert.enroll event = %+v, want tenant acme, outcome success", ev)
	}
}

// TestCertEnrollmentService_SignCertificate_FailClosedOnAuditRefusal pins
// the audit failure policy: under the default (fail-closed), an issuance
// whose GATING record cannot be written is refused and nothing is signed.
func TestCertEnrollmentService_SignCertificate_FailClosedOnAuditRefusal(t *testing.T) {
	ctx := context.Background()
	users := newCertEnrollmentUserStore(t)
	provider := newCertEnrollmentProvider(t)
	svc := NewCertEnrollmentService(provider, users, nil, WithEnrollmentAuditLogger(&refusingLogger{}))

	createTestAccount(t, users, "acme", "alice")
	_, csrPEM, err := certtest.NewCSR("alice")
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.SignCertificate(ctx, &SignCertificateRequest{
		TenantID: "acme", Username: "alice", CSRPEM: csrPEM, TTL: time.Hour,
	})
	var gateErr *audit.GateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("SignCertificate over a refusing trail = %v, want *audit.GateError", err)
	}
}

// refusingLogger is an audit.AuditLogger whose Emit always fails, as a
// full disk would.
type refusingLogger struct{}

func (r *refusingLogger) Emit(context.Context, audit.AuditEvent) error {
	return errors.New("audit sink unavailable")
}
func (r *refusingLogger) Query(context.Context, audit.Filter) (*audit.Page, error) {
	return nil, errors.New("audit sink unavailable")
}
func (r *refusingLogger) Close() error { return nil }

var _ audit.AuditLogger = (*refusingLogger)(nil)

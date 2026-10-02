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

package custodian_test

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
)

// recordingAudit keeps every event it is given.
type recordingAudit struct {
	mu     sync.Mutex
	events []audit.AuditEvent
}

func (r *recordingAudit) Emit(_ context.Context, ev audit.AuditEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	return nil
}

func (r *recordingAudit) Query(context.Context, audit.Filter) (*audit.Page, error) {
	return &audit.Page{}, nil
}

func (r *recordingAudit) Close() error { return nil }

// TestCustodianEvents_NameTheCustodiansTenant: an audit query is confined to
// the caller's tenant, so an event naming no tenant is a platform event only
// the system tenant reads. Every event of a custodian's registration and
// revocation names the custodian's tenant -- refusals included -- so that
// tenant's auditors see them.
func TestCustodianEvents_NameTheCustodiansTenant(t *testing.T) {
	rec := &recordingAudit{}
	svc, err := custodian.NewService(newMemKV(), rec, slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError})))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()

	req, _ := testRegisterRequest(t)
	resp, err := svc.RegisterCustodian(ctx, req)
	if err != nil {
		t.Fatalf("RegisterCustodian: %v", err)
	}
	dup, _ := testRegisterRequest(t)
	if _, err := svc.RegisterCustodian(ctx, dup); err == nil {
		t.Fatal("a duplicate registration was accepted")
	}
	invalid, _ := testRegisterRequest(t)
	invalid.PublicKeyPEM = ""
	if _, err := svc.RegisterCustodian(ctx, invalid); err == nil {
		t.Fatal("a registration without a public key was accepted")
	}
	if err := svc.RevokeCustodian(ctx, resp.Custodian.ID); err != nil {
		t.Fatalf("RevokeCustodian: %v", err)
	}

	if len(rec.events) < 4 {
		t.Fatalf("recorded %d events, want register, duplicate, invalid and revoke", len(rec.events))
	}
	for _, ev := range rec.events {
		if ev.TenantID != req.TenantID {
			t.Errorf("event %s/%s %q names tenant %q, want %q", ev.Resource, ev.Action, ev.Outcome, ev.TenantID, req.TenantID)
		}
	}
}

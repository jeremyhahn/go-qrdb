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

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// recordingStatusAuditWiring records the auditor server.New wires into the
// tenant service. It embeds a nil TenantServicer: server.New calls nothing on
// it but SetStatusChangeAuditor.
type recordingStatusAuditWiring struct {
	tenant.TenantServicer
	auditor tenant.StatusChangeAuditor
	calls   int
}

func (w *recordingStatusAuditWiring) SetStatusChangeAuditor(a tenant.StatusChangeAuditor) {
	w.auditor = a
	w.calls++
}

var _ tenant.StatusAuditWiring = (*recordingStatusAuditWiring)(nil)

// downAuditLogger refuses every write.
type downAuditLogger struct{}

var _ audit.AuditLogger = downAuditLogger{}

func (downAuditLogger) Emit(context.Context, audit.AuditEvent) error {
	return errors.New("audit sink unavailable")
}
func (downAuditLogger) Query(context.Context, audit.Filter) (*audit.Page, error) {
	return &audit.Page{}, nil
}
func (downAuditLogger) Close() error { return nil }

// server.New wires the status-change auditor over the node's audit logger
// and its configured failure policy, so a change through any transport is
// recorded under the posture the operator chose.
func TestNew_WiresTheTenantStatusAudit(t *testing.T) {
	for policy, refused := range map[string]bool{"fail-closed": true, "fail-open": false, "no-such-policy": true} {
		t.Run(policy, func(t *testing.T) {
			cfg := testConfig(t, false, false)
			cfg.Auth.AuditFailurePolicy = policy
			w := &recordingStatusAuditWiring{}
			New(cfg, newMockKVService(), &mockAdminService{}, WithTenantService(w), WithAuditLogger(downAuditLogger{}))
			if w.calls != 1 || w.auditor == nil {
				t.Fatalf("auditor wired %d times (nil: %t), want once", w.calls, w.auditor == nil)
			}
			_, err := w.auditor.BeginStatusChange(context.Background(), "acme", tenant.StatusActive, tenant.StatusSuspended)
			var gate *audit.GateError
			if got := errors.As(err, &gate); got != refused {
				t.Fatalf("BeginStatusChange with a down sink = %v; refused %t, want %t", err, got, refused)
			}
		})
	}
}

// With no audit logger there is nothing to record to; the auditor stays
// unwired.
func TestNew_NoAuditLoggerLeavesTheStatusAuditUnwired(t *testing.T) {
	w := &recordingStatusAuditWiring{}
	New(testConfig(t, false, false), newMockKVService(), &mockAdminService{}, WithTenantService(w))
	if w.calls != 0 {
		t.Fatalf("auditor wired %d times with no audit logger, want 0", w.calls)
	}
}

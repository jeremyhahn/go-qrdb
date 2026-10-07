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

package store

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
)

// TestPebbleStateMachine_Lookup_AuditQueryRequest exercises the Lookup
// dispatch for audit.QueryRequest, the propose-free read path every
// transport's "read the replicated chain" capability takes.
func TestPebbleStateMachine_Lookup_AuditQueryRequest(t *testing.T) {
	s, _ := openStoreWithAuditChain(t)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: auditAppendCmd(t, "acme", auditEvent("acme"))}})

	result, err := s.Lookup(context.Background(), audit.QueryRequest{TenantID: "acme", Filter: audit.Filter{Limit: 100}})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	page, ok := result.(*audit.Page)
	if !ok {
		t.Fatalf("Lookup result = %T, want *audit.Page", result)
	}
	if len(page.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(page.Events))
	}
}

// TestPebbleStateMachine_Lookup_AuditQueryRequest_NoChainConfigured asserts
// the query path refuses cleanly, like every other unconfigured-subsystem
// query, rather than panicking on a nil Chain.
func TestPebbleStateMachine_Lookup_AuditQueryRequest_NoChainConfigured(t *testing.T) {
	s := openExtendedStore(t)
	if _, err := s.Lookup(context.Background(), audit.QueryRequest{TenantID: "acme"}); err == nil {
		t.Fatal("expected an error with no audit chain configured")
	}
}

// TestPebbleStateMachine_Lookup_AuditVerifyRequest exercises the Lookup
// dispatch for audit.VerifyRequest, the hash-continuity check exposed to
// auditors.
func TestPebbleStateMachine_Lookup_AuditVerifyRequest(t *testing.T) {
	s, _ := openStoreWithAuditChain(t)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: auditAppendCmd(t, "acme", auditEvent("acme"))}})

	result, err := s.Lookup(context.Background(), audit.VerifyRequest{TenantID: "acme"})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	report, ok := result.(*audit.ChainContinuityReport)
	if !ok {
		t.Fatalf("Lookup result = %T, want *audit.ChainContinuityReport", result)
	}
	if !report.Verified || report.Records != 1 {
		t.Fatalf("report = %+v", report)
	}
}

// TestPebbleStateMachine_Lookup_AuditVerifyRequest_NoChainConfigured asserts
// the verify path refuses cleanly with no audit chain configured.
func TestPebbleStateMachine_Lookup_AuditVerifyRequest_NoChainConfigured(t *testing.T) {
	s := openExtendedStore(t)
	if _, err := s.Lookup(context.Background(), audit.VerifyRequest{TenantID: "acme"}); err == nil {
		t.Fatal("expected an error with no audit chain configured")
	}
}

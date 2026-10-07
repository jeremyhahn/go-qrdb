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

package engine

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
)

// TestMVCCEngine_Lookup_AuditQueryRequest exercises the Lookup dispatch for
// audit.QueryRequest, the propose-free read path every transport's "read
// the replicated chain" capability takes.
func TestMVCCEngine_Lookup_AuditQueryRequest(t *testing.T) {
	eng, _ := newMVCCEngine(t, WithMVCCAuditChain(audit.NewChain()))
	entries := []sm.Entry{auditAppendEntry(t, 1, "acme", tenantEvent("acme", "barrier", "unseal"))}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("append: %v", err)
	}

	result, err := eng.Lookup(context.Background(), audit.QueryRequest{TenantID: "acme", Filter: audit.Filter{Limit: 100}})
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

// TestMVCCEngine_Lookup_AuditQueryRequest_NoChainConfigured asserts the
// query path refuses cleanly with no audit chain configured.
func TestMVCCEngine_Lookup_AuditQueryRequest_NoChainConfigured(t *testing.T) {
	eng, _ := newMVCCEngine(t)
	if _, err := eng.Lookup(context.Background(), audit.QueryRequest{TenantID: "acme"}); err == nil {
		t.Fatal("expected an error with no audit chain configured")
	}
}

// TestMVCCEngine_Lookup_AuditVerifyRequest exercises the Lookup dispatch for
// audit.VerifyRequest, the hash-continuity check exposed to auditors.
func TestMVCCEngine_Lookup_AuditVerifyRequest(t *testing.T) {
	eng, _ := newMVCCEngine(t, WithMVCCAuditChain(audit.NewChain()))
	entries := []sm.Entry{auditAppendEntry(t, 1, "acme", tenantEvent("acme", "barrier", "unseal"))}
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("append: %v", err)
	}

	result, err := eng.Lookup(context.Background(), audit.VerifyRequest{TenantID: "acme"})
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

// TestMVCCEngine_Lookup_AuditVerifyRequest_NoChainConfigured asserts the
// verify path refuses cleanly with no audit chain configured.
func TestMVCCEngine_Lookup_AuditVerifyRequest_NoChainConfigured(t *testing.T) {
	eng, _ := newMVCCEngine(t)
	if _, err := eng.Lookup(context.Background(), audit.VerifyRequest{TenantID: "acme"}); err == nil {
		t.Fatal("expected an error with no audit chain configured")
	}
}

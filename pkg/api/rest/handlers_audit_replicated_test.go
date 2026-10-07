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

package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// stubReplicatedInner implements audit.AuditLogger and
// service.TenantChainVerifier, standing in for a *service.ReplicatedLogger
// without a real Host.
type stubReplicatedInner struct {
	stubAuditLogger
	verifyReport *audit.ChainContinuityReport
	verifyErr    error
	verifiedFor  []string
}

func (s *stubReplicatedInner) VerifyTenantChain(_ context.Context, tenantID string) (*audit.ChainContinuityReport, error) {
	s.verifiedFor = append(s.verifiedFor, tenantID)
	return s.verifyReport, s.verifyErr
}

// stubEntitlementLookup answers ReplicatedAuditEnabled from a fixed map.
type stubEntitlementLookup struct {
	enabled map[string]bool
}

func (s *stubEntitlementLookup) ReplicatedAuditEnabled(_ context.Context, tenantID string) (bool, error) {
	return s.enabled[tenantID], nil
}

func TestHandleAuditQuery_Replicated_NilNotImplemented(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodGet, "/v1/audit/events?replicated=true", nil)
	rec := httptest.NewRecorder()
	srv.handleAuditQuery(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("code = %d, want 501", rec.Code)
	}
}

func TestHandleAuditQuery_Replicated_EntitledReadsReplicatedLogger(t *testing.T) {
	inner := &stubReplicatedInner{stubAuditLogger: stubAuditLogger{page: &audit.Page{Events: []audit.AuditEvent{{Actor: "op"}}, Total: -1}}}
	nodeLocal := &stubAuditLogger{page: &audit.Page{Events: []audit.AuditEvent{{Actor: "should-not-be-read"}}, Total: 0}}
	replicated := service.NewOptInAuditLogger(inner, &stubEntitlementLookup{enabled: map[string]bool{"acme": true}}, false)
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuditLogger(nodeLocal), WithReplicatedAuditLogger(replicated))

	req := httptest.NewRequest(http.MethodGet, "/v1/audit/events?replicated=true&tenant_id=acme", nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), &auth.Identity{Subject: "a", TenantID: "acme"}))
	rec := httptest.NewRecorder()
	srv.handleAuditQuery(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp auditQueryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Events) != 1 || resp.Events[0].Actor != "op" {
		t.Fatalf("got %+v, want the replicated logger's event", resp.Events)
	}
	if len(inner.queried) != 1 {
		t.Fatalf("replicated logger queried %d times, want 1", len(inner.queried))
	}
	if len(nodeLocal.queried) != 0 {
		t.Fatalf("node-local logger queried %d times, want 0 (replicated=true must not read it)", len(nodeLocal.queried))
	}
}

func TestHandleAuditQuery_Replicated_NotEntitledAnswersEmptyNotAnError(t *testing.T) {
	inner := &stubReplicatedInner{}
	replicated := service.NewOptInAuditLogger(inner, &stubEntitlementLookup{enabled: map[string]bool{}}, false)
	srv := NewServer(nil, nil, "127.0.0.1:0", WithReplicatedAuditLogger(replicated))

	req := httptest.NewRequest(http.MethodGet, "/v1/audit/events?replicated=true&tenant_id=acme", nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), &auth.Identity{Subject: "a", TenantID: "acme"}))
	rec := httptest.NewRecorder()
	srv.handleAuditQuery(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp auditQueryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Total != -1 || len(resp.Events) != 0 {
		t.Fatalf("got %+v, want Total: -1, no events", resp)
	}
}

func TestHandleAuditQuery_Replicated_ForeignTenantIsForbidden(t *testing.T) {
	inner := &stubReplicatedInner{}
	replicated := service.NewOptInAuditLogger(inner, &stubEntitlementLookup{enabled: map[string]bool{"acme": true}}, false)
	srv := NewServer(nil, nil, "127.0.0.1:0", WithReplicatedAuditLogger(replicated))

	req := httptest.NewRequest(http.MethodGet, "/v1/audit/events?replicated=true&tenant_id=acme", nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), &auth.Identity{Subject: "a", TenantID: "widgets"}))
	rec := httptest.NewRecorder()
	srv.handleAuditQuery(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
	if len(inner.queried) != 0 {
		t.Fatalf("replicated logger queried %d times, want 0 (refused before reaching it)", len(inner.queried))
	}
}

func TestHandleAuditVerifyReplicated_NilNotImplemented(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")
	req := httptest.NewRequest(http.MethodGet, "/v1/audit/replicated/verify", nil)
	rec := httptest.NewRecorder()
	srv.handleAuditVerifyReplicated(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("code = %d, want 501", rec.Code)
	}
}

func TestHandleAuditVerifyReplicated_Success(t *testing.T) {
	inner := &stubReplicatedInner{verifyReport: &audit.ChainContinuityReport{Verified: true, Records: 3, Head: 3, Low: 1, Genesis: true}}
	replicated := service.NewOptInAuditLogger(inner, &stubEntitlementLookup{enabled: map[string]bool{"acme": true}}, false)
	srv := NewServer(nil, nil, "127.0.0.1:0", WithReplicatedAuditLogger(replicated))

	req := httptest.NewRequest(http.MethodGet, "/v1/audit/replicated/verify", nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), &auth.Identity{Subject: "a", TenantID: "acme"}))
	rec := httptest.NewRecorder()
	srv.handleAuditVerifyReplicated(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var report audit.ChainContinuityReport
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !report.Verified || report.Records != 3 {
		t.Fatalf("got %+v", report)
	}
	if len(inner.verifiedFor) != 1 || inner.verifiedFor[0] != "acme" {
		t.Fatalf("verifiedFor = %v, want [acme]", inner.verifiedFor)
	}
}

func TestHandleAuditVerifyReplicated_NotEntitled(t *testing.T) {
	inner := &stubReplicatedInner{}
	replicated := service.NewOptInAuditLogger(inner, &stubEntitlementLookup{enabled: map[string]bool{}}, false)
	srv := NewServer(nil, nil, "127.0.0.1:0", WithReplicatedAuditLogger(replicated))

	req := httptest.NewRequest(http.MethodGet, "/v1/audit/replicated/verify", nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), &auth.Identity{Subject: "a", TenantID: "acme"}))
	rec := httptest.NewRecorder()
	srv.handleAuditVerifyReplicated(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403 (not entitled)", rec.Code)
	}
}

func TestHandleAuditVerifyReplicated_ForeignTenantIsForbidden(t *testing.T) {
	inner := &stubReplicatedInner{}
	replicated := service.NewOptInAuditLogger(inner, &stubEntitlementLookup{enabled: map[string]bool{"acme": true}}, false)
	srv := NewServer(nil, nil, "127.0.0.1:0", WithReplicatedAuditLogger(replicated))

	req := httptest.NewRequest(http.MethodGet, "/v1/audit/replicated/verify?tenant_id=acme", nil)
	req = req.WithContext(auth.WithIdentity(req.Context(), &auth.Identity{Subject: "a", TenantID: "widgets"}))
	rec := httptest.NewRecorder()
	srv.handleAuditVerifyReplicated(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
	if len(inner.verifiedFor) != 0 {
		t.Fatal("verify reached inner despite the cross-tenant refusal")
	}
}

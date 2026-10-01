// Copyright 2026 Jeremy Hahn
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

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// restTenantFactor is a trivial authfactors.Factor used only to exercise the
// REAL tenant-routed AuthFactorServicer through the REST handler. It never
// performs real cryptographic operations.
type restTenantFactor struct{}

func (restTenantFactor) Name() string { return "fake" }

func (restTenantFactor) Enroll(_ context.Context, p *authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	return &authfactors.Enrollment{
		EnrollmentID: "rest-enroll-" + p.UserID,
		FactorName:   "fake",
		UserID:       p.UserID,
		TenantID:     p.TenantID,
		EnrolledAt:   time.Now().UTC(),
	}, nil
}

func (restTenantFactor) Challenge(_ context.Context, e *authfactors.Enrollment) (*authfactors.Challenge, error) {
	return &authfactors.Challenge{ChallengeID: "rest-challenge-" + e.EnrollmentID, EnrollmentID: e.EnrollmentID}, nil
}

func (restTenantFactor) Verify(context.Context, *authfactors.Challenge, *authfactors.Response) error {
	return nil
}

var _ authfactors.Factor = restTenantFactor{}

// newRealRoutedAuthFactorService builds the PRODUCTION tenant-routed
// AuthFactorServicer over an in-memory store, provisioning the named
// tenants. The claim under test — that the REST transport surfaces the
// routing layer's tenant mismatch refusal — is a property of the real
// resolver and the real router, so a stub service would test the stub.
func newRealRoutedAuthFactorService(t *testing.T, tenantIDs ...string) service.AuthFactorServicer {
	t.Helper()
	store := kvstore.NewMemoryKVStore()
	tenants, err := tenant.NewKVTenantService(store, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	for _, id := range tenantIDs {
		if _, err := tenants.CreateTenant(context.Background(), id, id, tenant.IsolationPrefix); err != nil {
			t.Fatalf("CreateTenant(%s): %v", id, err)
		}
	}
	resolver, err := tenant.NewStoreResolver(store, nil, tenants)
	if err != nil {
		t.Fatalf("NewStoreResolver: %v", err)
	}
	svc, err := service.NewRoutingAuthFactorService(service.RoutingAuthFactorServiceConfig{
		Factors:  []authfactors.Factor{restTenantFactor{}},
		Resolver: resolver,
	})
	if err != nil {
		t.Fatalf("NewRoutingAuthFactorService: %v", err)
	}
	return svc
}

// TestHandleAuthFactorEnroll_RefusesMismatchedTenantID proves the REST
// transport surfaces the routing layer's refusal end to end: a request whose
// context resolves to "acme" but whose JSON body names "globex" as the
// tenant is refused with 400, and nothing is enrolled in either tenant.
func TestHandleAuthFactorEnroll_RefusesMismatchedTenantID(t *testing.T) {
	svc := newRealRoutedAuthFactorService(t, "acme", "globex")
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuthFactorService(svc))

	body := `{"user_id":"custodian-spoof","tenant_id":"globex"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/authfactors/fake/enroll", bytes.NewReader([]byte(body)))
	req = req.WithContext(tenant.WithTenantID(req.Context(), "acme"))
	rec := httptest.NewRecorder()

	srv.handleAuthFactorEnroll(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("mismatched tenant_id = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}

	for _, tenantID := range []string{"acme", "globex"} {
		got, err := svc.ListFactors(tenant.WithTenantID(context.Background(), tenantID), "")
		if err != nil {
			t.Fatalf("ListFactors(%s): %v", tenantID, err)
		}
		if len(got) != 0 {
			t.Fatalf("ListFactors(%s) = %v, want none after a refused enrolment", tenantID, got)
		}
	}
}

// TestHandleAuthFactorEnroll_AllowsMatchingTenantID is the companion positive
// case: a request whose body names the same tenant its context resolves to
// succeeds and is reachable from that tenant only.
func TestHandleAuthFactorEnroll_AllowsMatchingTenantID(t *testing.T) {
	svc := newRealRoutedAuthFactorService(t, "acme", "globex")
	srv := NewServer(nil, nil, "127.0.0.1:0", WithAuthFactorService(svc))

	body := `{"user_id":"custodian-match","tenant_id":"acme"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/authfactors/fake/enroll", bytes.NewReader([]byte(body)))
	req = req.WithContext(tenant.WithTenantID(req.Context(), "acme"))
	rec := httptest.NewRecorder()

	srv.handleAuthFactorEnroll(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("matching tenant_id = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}
	var resp enrollFactorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.TenantID != "acme" {
		t.Fatalf("response TenantID = %q, want acme", resp.TenantID)
	}

	got, err := svc.ListFactors(tenant.WithTenantID(context.Background(), "globex"), "")
	if err != nil || len(got) != 0 {
		t.Fatalf("ListFactors(globex) = %v, %v, want none", got, err)
	}
}

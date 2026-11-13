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

// Field-level tests for the embedded transport's trust-anchor verbs and
// CreateTenant's admin-onboarding fields. IssueCert/EnrollCert's own
// field-level coverage lives in transport_auth_test.go and
// cert_confinement_test.go; this file is the remaining task-2 surface:
// AddTrustAnchors, ListTrustAnchors, RemoveTrustAnchor and the admin fields
// of CreateTenant/CreateTenantResponse.

package embedded

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// AddTrustAnchors / ListTrustAnchors / RemoveTrustAnchor
// ---------------------------------------------------------------------------

func TestAddTrustAnchors_Success(t *testing.T) {
	notBefore := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	notAfter := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	addedAt := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	var gotTenant, gotBundle string
	mock := &mockCertEnrollmentServicer{
		addTrustAnchorsFn: func(_ context.Context, tenantID string, bundlePEM []byte) ([]trustanchor.Anchor, error) {
			gotTenant, gotBundle = tenantID, string(bundlePEM)
			return []trustanchor.Anchor{{
				Fingerprint: "fp1", Subject: "CN=ca",
				NotBefore: notBefore, NotAfter: notAfter, CertPEM: []byte("ca-pem"), AddedAt: addedAt,
			}}, nil
		},
	}
	tr := newAuthTransport(t, WithCertEnrollment(mock))

	resp, err := tr.AddTrustAnchors(callerCtx("acme"), &transport.AddTrustAnchorsRequest{
		TenantID: "acme", CertsPEM: "bundle-pem",
	})
	if err != nil {
		t.Fatalf("AddTrustAnchors: %v", err)
	}
	if gotTenant != "acme" || gotBundle != "bundle-pem" {
		t.Errorf("service received tenant=%q bundle=%q; want acme/bundle-pem", gotTenant, gotBundle)
	}
	if len(resp.Anchors) != 1 {
		t.Fatalf("got %d anchors, want 1", len(resp.Anchors))
	}
	a := resp.Anchors[0]
	if a.Fingerprint != "fp1" || a.Subject != "CN=ca" || a.CertPEM != "ca-pem" {
		t.Errorf("anchor = %+v", a)
	}
	if !a.NotBefore.Equal(notBefore) || !a.NotAfter.Equal(notAfter) || !a.AddedAt.Equal(addedAt) {
		t.Errorf("anchor times = %+v", a)
	}
}

func TestAddTrustAnchors_NilService(t *testing.T) {
	tr := newAuthTransport(t)
	_, err := tr.AddTrustAnchors(context.Background(), &transport.AddTrustAnchorsRequest{TenantID: "acme"})
	assertNotImplementedError(t, err, "AddTrustAnchors")
}

func TestAddTrustAnchors_ServiceError(t *testing.T) {
	mock := &mockCertEnrollmentServicer{
		addTrustAnchorsFn: func(context.Context, string, []byte) ([]trustanchor.Anchor, error) {
			return nil, errors.New("invalid PEM bundle")
		},
	}
	tr := newAuthTransport(t, WithCertEnrollment(mock))
	_, err := tr.AddTrustAnchors(callerCtx("acme"), &transport.AddTrustAnchorsRequest{TenantID: "acme"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestListTrustAnchors_Success(t *testing.T) {
	var gotTenant string
	mock := &mockCertEnrollmentServicer{
		listTrustAnchorsFn: func(_ context.Context, tenantID string) ([]trustanchor.Anchor, error) {
			gotTenant = tenantID
			return []trustanchor.Anchor{{Fingerprint: "fp1"}, {Fingerprint: "fp2"}}, nil
		},
	}
	tr := newAuthTransport(t, WithCertEnrollment(mock))

	resp, err := tr.ListTrustAnchors(callerCtx("acme"), "acme")
	if err != nil {
		t.Fatalf("ListTrustAnchors: %v", err)
	}
	if gotTenant != "acme" {
		t.Errorf("service received tenant=%q, want acme", gotTenant)
	}
	if len(resp.Anchors) != 2 {
		t.Fatalf("got %d anchors, want 2", len(resp.Anchors))
	}
}

func TestListTrustAnchors_NilService(t *testing.T) {
	tr := newAuthTransport(t)
	_, err := tr.ListTrustAnchors(context.Background(), "acme")
	assertNotImplementedError(t, err, "ListTrustAnchors")
}

func TestRemoveTrustAnchor_Success(t *testing.T) {
	var gotTenant, gotFP string
	mock := &mockCertEnrollmentServicer{
		removeTrustAnchorFn: func(_ context.Context, tenantID, fingerprint string) error {
			gotTenant, gotFP = tenantID, fingerprint
			return nil
		},
	}
	tr := newAuthTransport(t, WithCertEnrollment(mock))

	if err := tr.RemoveTrustAnchor(callerCtx("acme"), "acme", "fp1"); err != nil {
		t.Fatalf("RemoveTrustAnchor: %v", err)
	}
	if gotTenant != "acme" || gotFP != "fp1" {
		t.Errorf("service received tenant=%q fingerprint=%q; want acme/fp1", gotTenant, gotFP)
	}
}

func TestRemoveTrustAnchor_NilService(t *testing.T) {
	tr := newAuthTransport(t)
	err := tr.RemoveTrustAnchor(context.Background(), "acme", "fp1")
	assertNotImplementedError(t, err, "RemoveTrustAnchor")
}

func TestRemoveTrustAnchor_ServiceError(t *testing.T) {
	mock := &mockCertEnrollmentServicer{
		removeTrustAnchorFn: func(context.Context, string, string) error {
			return errors.New("no such anchor")
		},
	}
	tr := newAuthTransport(t, WithCertEnrollment(mock))
	err := tr.RemoveTrustAnchor(callerCtx("acme"), "acme", "nope")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestTrustAnchors_CrossTenantDenied: the transport refuses another tenant's
// trust anchors before it dispatches to the cert-enrollment service. The
// injected service fails the test if any verb reaches it, so this pins the
// transport's own confinement, which the real service's check would mask.
func TestTrustAnchors_CrossTenantDenied(t *testing.T) {
	reached := map[string]bool{}
	mock := &mockCertEnrollmentServicer{
		addTrustAnchorsFn: func(context.Context, string, []byte) ([]trustanchor.Anchor, error) {
			reached["AddTrustAnchors"] = true
			return nil, nil
		},
		listTrustAnchorsFn: func(context.Context, string) ([]trustanchor.Anchor, error) {
			reached["ListTrustAnchors"] = true
			return nil, nil
		},
		removeTrustAnchorFn: func(context.Context, string, string) error {
			reached["RemoveTrustAnchor"] = true
			return nil
		},
	}
	tr := newAuthTransport(t, WithCertEnrollment(mock))
	ctx := callerCtx("acme")

	verbs := []struct {
		name string
		call func() error
	}{
		{"AddTrustAnchors", func() error {
			_, err := tr.AddTrustAnchors(ctx, &transport.AddTrustAnchorsRequest{TenantID: "globex"})
			return err
		}},
		{"ListTrustAnchors", func() error {
			_, err := tr.ListTrustAnchors(ctx, "globex")
			return err
		}},
		{"RemoveTrustAnchor", func() error {
			return tr.RemoveTrustAnchor(ctx, "globex", "fp1")
		}},
	}
	for _, v := range verbs {
		t.Run(v.name, func(t *testing.T) {
			err := v.call()
			if reached[v.name] {
				t.Fatalf("%s: the service must not be reached for a cross-tenant request", v.name)
			}
			var denied *tenant.CrossTenantDeniedError
			if !errors.As(err, &denied) {
				t.Fatalf("err = %v (%T); want *tenant.CrossTenantDeniedError", err, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CreateTenant admin fields
// ---------------------------------------------------------------------------

// TestCreateTenant_AdminFieldsRoundTrip proves every admin-onboarding field
// crosses the embedded boundary in both directions: the request's
// AdminUsername/AdminCSRPEM/AdminChainPEM/TrustAnchorsPEM all reach
// OnboardTenantRequest, and the response's TenantAdminCredential fields,
// NotAfter/AwaitsBarrierInit included, decode back out.
func TestCreateTenant_AdminFieldsRoundTrip(t *testing.T) {
	var got *service.OnboardTenantRequest
	mock := &mockTenantOnboardingServicer{
		onboardTenantFn: func(_ context.Context, req *service.OnboardTenantRequest) (*service.OnboardTenantResult, error) {
			got = req
			return &service.OnboardTenantResult{
				Tenant: &tenant.TenantInfo{ID: req.ID, Name: req.Name, IsolationType: req.IsolationType},
				Admin: service.TenantAdminCredential{
					UserID:     7,
					Username:   req.AdminUsername,
					CertPEM:    []byte("admin-cert-pem"),
					Serial:     "admin-serial",
					Thumbprint: "admin-thumb",
				},
			}, nil
		},
	}
	tr := newAuthTransport(t, WithTenantOnboarding(mock))

	resp, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{
		ID:              "acme",
		Name:            "Acme Corp",
		IsolationType:   transport.TenantIsolationBarrier,
		AdminUsername:   "admin",
		AdminCSRPEM:     "admin-csr-pem",
		AdminCertPEM:    "admin-cert-pem-in",
		AdminChainPEM:   "admin-chain-pem",
		TrustAnchorsPEM: "anchors-pem",
	})
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if got.AdminUsername != "admin" || string(got.AdminCSRPEM) != "admin-csr-pem" ||
		string(got.AdminCertPEM) != "admin-cert-pem-in" || string(got.AdminChainPEM) != "admin-chain-pem" ||
		string(got.TrustAnchorsPEM) != "anchors-pem" {
		t.Errorf("OnboardTenantRequest = %+v", got)
	}
	if resp.Admin.UserID != 7 || resp.Admin.Username != "admin" || resp.Admin.CertPEM != "admin-cert-pem" ||
		resp.Admin.Serial != "admin-serial" || resp.Admin.Thumbprint != "admin-thumb" {
		t.Errorf("Admin = %+v", resp.Admin)
	}
	if resp.Admin.AwaitsBarrierInit {
		t.Error("AwaitsBarrierInit = true, want false")
	}
}

// TestCreateTenant_AwaitsBarrierInit proves the dedicated-barrier onboarding
// path -- a zero UserID alongside AwaitsBarrierInit=true -- decodes without
// the zero UserID being read as "field omitted".
func TestCreateTenant_AwaitsBarrierInit(t *testing.T) {
	mock := &mockTenantOnboardingServicer{
		onboardTenantFn: func(_ context.Context, req *service.OnboardTenantRequest) (*service.OnboardTenantResult, error) {
			return &service.OnboardTenantResult{
				Tenant: &tenant.TenantInfo{ID: req.ID, Name: req.Name},
				Admin: service.TenantAdminCredential{
					Username:          req.AdminUsername,
					CertPEM:           []byte("admin-cert-pem"),
					AwaitsBarrierInit: true,
				},
			}, nil
		},
	}
	tr := newAuthTransport(t, WithTenantOnboarding(mock))

	resp, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{
		ID: "acme", Name: "Acme Corp", AdminUsername: "admin", AdminCSRPEM: "csr",
	})
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if !resp.Admin.AwaitsBarrierInit {
		t.Fatal("AwaitsBarrierInit = false, want true")
	}
	if resp.Admin.UserID != 0 {
		t.Fatalf("UserID = %d, want 0 while awaiting barrier init", resp.Admin.UserID)
	}
}

func TestCreateTenant_OnboardingServiceError(t *testing.T) {
	mock := &mockTenantOnboardingServicer{
		onboardTenantFn: func(context.Context, *service.OnboardTenantRequest) (*service.OnboardTenantResult, error) {
			return nil, errors.New("admin_username is required")
		},
	}
	tr := newAuthTransport(t, WithTenantOnboarding(mock))
	_, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{ID: "acme", Name: "Acme"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// A create of an undone id a serving node has not yet released names its
// retry delay, as the remote transports carry it, and keeps the typed error.
func TestCreateTenant_UndoPendingCarriesTheRetryDelay(t *testing.T) {
	mock := &mockTenantOnboardingServicer{
		onboardTenantFn: func(context.Context, *service.OnboardTenantRequest) (*service.OnboardTenantResult, error) {
			return nil, &tenant.TenantUndoPendingError{TenantID: "acme", Pending: []string{"n2:7000"}, RetryAfter: tenant.UndoPendingRetryAfter}
		},
	}
	tr := newAuthTransport(t, WithTenantOnboarding(mock))
	_, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{ID: "acme", Name: "Acme"})
	var reqErr *transport.RequestError
	var pending *tenant.TenantUndoPendingError
	if !errors.As(err, &reqErr) || !errors.As(err, &pending) {
		t.Fatalf("CreateTenant = %v, want a RequestError around *tenant.TenantUndoPendingError", err)
	}
	if reqErr.RetryAfter != tenant.UndoPendingRetryAfter || reqErr.Method != "CreateTenant" {
		t.Fatalf("RequestError = %+v, want RetryAfter %v on CreateTenant", reqErr, tenant.UndoPendingRetryAfter)
	}
}

// A refusal naming no delay is returned as it is.
func TestCreateTenant_ErrorNamingNoDelayIsUnwrapped(t *testing.T) {
	boom := errors.New("admin_username is required")
	mock := &mockTenantOnboardingServicer{
		onboardTenantFn: func(context.Context, *service.OnboardTenantRequest) (*service.OnboardTenantResult, error) {
			return nil, boom
		},
	}
	tr := newAuthTransport(t, WithTenantOnboarding(mock))
	if _, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{ID: "acme", Name: "Acme"}); err != boom {
		t.Fatalf("CreateTenant = %v, want %v unchanged", err, boom)
	}
}

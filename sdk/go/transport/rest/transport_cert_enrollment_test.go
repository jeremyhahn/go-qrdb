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

// Field-level request-encoding / response-decoding / error-mapping tests for
// the certificate enrolment and tenant onboarding verbs: IssueCert,
// EnrollCert, AddTrustAnchors, ListTrustAnchors, RemoveTrustAnchor and
// CreateTenant's admin fields. transport_coverage_test.go and
// transport_error_paths_test.go already cover the closed/server-error
// boilerplate for these verbs; this file is the wire-shape contract.

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// IssueCert / EnrollCert
// ---------------------------------------------------------------------------

func TestRESTIssueCert_RequestAndResponseFields(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	var captured transport.IssueCertRequest
	notAfter := time.Date(2027, 5, 6, 7, 8, 9, 0, time.UTC)
	rs.handle(exactPath(http.MethodPost, "/certs/issue"), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		writeJSON(w, &transport.EnrolledCert{
			UserID: 99, Username: "alice", TenantID: "acme",
			CertPEM: "cert-pem", Serial: "ser-1", Thumbprint: "thumb-1",
			NotAfter: notAfter,
		})
	})
	enrolled, err := rt.IssueCert(context.Background(), &transport.IssueCertRequest{
		Username: "alice", CSRPEM: "csr-bytes", TTL: "24h",
	})
	if err != nil {
		t.Fatalf("IssueCert: %v", err)
	}
	if captured.Username != "alice" || captured.CSRPEM != "csr-bytes" || captured.TTL != "24h" {
		t.Fatalf("request captured = %+v", captured)
	}
	if enrolled.UserID != 99 || enrolled.Username != "alice" || enrolled.TenantID != "acme" ||
		enrolled.CertPEM != "cert-pem" || enrolled.Serial != "ser-1" || enrolled.Thumbprint != "thumb-1" {
		t.Fatalf("response = %+v", enrolled)
	}
	if !enrolled.NotAfter.Equal(notAfter) {
		t.Fatalf("NotAfter = %v, want %v", enrolled.NotAfter, notAfter)
	}
}

func TestRESTIssueCert_NotFoundMapsToTypedStatus(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/certs/issue"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "user: not found: acme/nobody", http.StatusNotFound)
	})
	_, err := rt.IssueCert(context.Background(), &transport.IssueCertRequest{Username: "nobody", CSRPEM: "csr"})
	if err == nil {
		t.Fatal("expected error")
	}
	// A 404 from IssueCert can only be the account, so it is typed.
	var unf *transport.UserNotFoundError
	if !errors.As(err, &unf) || unf.Lookup != "nobody" {
		t.Fatalf("err = %v (%T); want *transport.UserNotFoundError naming nobody", err, err)
	}
}

func TestRESTIssueCert_OtherStatusesStayRequestErrors(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/certs/issue"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad csr", http.StatusBadRequest)
	})
	_, err := rt.IssueCert(context.Background(), &transport.IssueCertRequest{Username: "nobody", CSRPEM: "csr"})
	var re *transport.RequestError
	if !errors.As(err, &re) || re.StatusCode != http.StatusBadRequest {
		t.Fatalf("err = %v (%T); want a *transport.RequestError carrying 400", err, err)
	}
}

func TestRESTEnrollCert_RequestAndResponseFields(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	var captured transport.EnrollCertRequest
	rs.handle(exactPath(http.MethodPost, "/certs/enroll"), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		writeJSON(w, &transport.EnrolledCert{
			Username: "alice", TenantID: "acme", CertPEM: "leaf-pem", Thumbprint: "thumb-byoc",
		})
	})
	enrolled, err := rt.EnrollCert(context.Background(), &transport.EnrollCertRequest{
		Username: "alice", CertPEM: "leaf-pem", ChainPEM: "chain-pem",
	})
	if err != nil {
		t.Fatalf("EnrollCert: %v", err)
	}
	if captured.Username != "alice" || captured.CertPEM != "leaf-pem" || captured.ChainPEM != "chain-pem" {
		t.Fatalf("request captured = %+v", captured)
	}
	if enrolled.Serial != "" {
		t.Fatalf("Serial = %q, want empty for a bring-your-own certificate", enrolled.Serial)
	}
	if enrolled.Thumbprint != "thumb-byoc" {
		t.Fatalf("Thumbprint = %q, want thumb-byoc", enrolled.Thumbprint)
	}
}

func TestRESTEnrollCert_RefusedMapsToTypedStatus(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/certs/enroll"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "chain does not verify against a registered trust anchor", http.StatusBadRequest)
	})
	_, err := rt.EnrollCert(context.Background(), &transport.EnrollCertRequest{Username: "alice", CertPEM: "bad-pem"})
	if err == nil {
		t.Fatal("expected error")
	}
	var re *transport.RequestError
	if !errors.As(err, &re) || re.StatusCode != http.StatusBadRequest {
		t.Fatalf("err = %v (%T); want a *transport.RequestError carrying 400", err, err)
	}
}

// ---------------------------------------------------------------------------
// Trust anchors
// ---------------------------------------------------------------------------

func TestRESTAddTrustAnchors_RequestAndResponseFields(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	var capturedBody struct {
		CertsPEM string `json:"certs_pem"`
	}
	var capturedPath string
	notBefore := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	notAfter := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	addedAt := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	rs.handle(exactPath(http.MethodPost, "/v1/tenants/acme/trust-anchors"), func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		writeJSON(w, &transport.TrustAnchorList{Anchors: []transport.TrustAnchor{{
			Fingerprint: "fp1", Subject: "CN=ca", CertPEM: "ca-pem",
			NotBefore: notBefore, NotAfter: notAfter, AddedAt: addedAt,
		}}})
	})
	resp, err := rt.AddTrustAnchors(context.Background(), &transport.AddTrustAnchorsRequest{
		TenantID: "acme", CertsPEM: "bundle-pem",
	})
	if err != nil {
		t.Fatalf("AddTrustAnchors: %v", err)
	}
	if capturedPath != "/v1/tenants/acme/trust-anchors" {
		t.Fatalf("path = %q", capturedPath)
	}
	if capturedBody.CertsPEM != "bundle-pem" {
		t.Fatalf("request body certs_pem = %q, want bundle-pem", capturedBody.CertsPEM)
	}
	if len(resp.Anchors) != 1 {
		t.Fatalf("got %d anchors, want 1", len(resp.Anchors))
	}
	a := resp.Anchors[0]
	if a.Fingerprint != "fp1" || a.Subject != "CN=ca" || a.CertPEM != "ca-pem" {
		t.Fatalf("anchor = %+v", a)
	}
	if !a.NotBefore.Equal(notBefore) || !a.NotAfter.Equal(notAfter) || !a.AddedAt.Equal(addedAt) {
		t.Fatalf("anchor times = %+v", a)
	}
}

func TestRESTListTrustAnchors_PathAndResponse(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	var capturedPath string
	rs.handle(exactPath(http.MethodGet, "/v1/tenants/acme/trust-anchors"), func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		writeJSON(w, &transport.TrustAnchorList{Anchors: []transport.TrustAnchor{
			{Fingerprint: "fp1"}, {Fingerprint: "fp2"},
		}})
	})
	resp, err := rt.ListTrustAnchors(context.Background(), "acme")
	if err != nil {
		t.Fatalf("ListTrustAnchors: %v", err)
	}
	if capturedPath != "/v1/tenants/acme/trust-anchors" {
		t.Fatalf("path = %q", capturedPath)
	}
	if len(resp.Anchors) != 2 {
		t.Fatalf("got %d anchors, want 2", len(resp.Anchors))
	}
}

func TestRESTRemoveTrustAnchor_Path(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	var capturedPath, capturedMethod string
	rs.handle(exactPath(http.MethodDelete, "/v1/tenants/acme/trust-anchors/fp1"), func(w http.ResponseWriter, r *http.Request) {
		capturedPath, capturedMethod = r.URL.Path, r.Method
		w.WriteHeader(http.StatusNoContent)
	})
	if err := rt.RemoveTrustAnchor(context.Background(), "acme", "fp1"); err != nil {
		t.Fatalf("RemoveTrustAnchor: %v", err)
	}
	if capturedMethod != http.MethodDelete || capturedPath != "/v1/tenants/acme/trust-anchors/fp1" {
		t.Fatalf("method/path = %q %q", capturedMethod, capturedPath)
	}
}

func TestRESTRemoveTrustAnchor_NotFoundMapsToTypedStatus(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodDelete, "/v1/tenants/acme/trust-anchors/nope"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no such trust anchor", http.StatusNotFound)
	})
	err := rt.RemoveTrustAnchor(context.Background(), "acme", "nope")
	if err == nil {
		t.Fatal("expected error")
	}
	var re *transport.RequestError
	if !errors.As(err, &re) || re.StatusCode != http.StatusNotFound {
		t.Fatalf("err = %v (%T); want a *transport.RequestError carrying 404", err, err)
	}
}

// ---------------------------------------------------------------------------
// CreateTenant admin fields
// ---------------------------------------------------------------------------

// TestRESTCreateTenant_AdminFieldsRoundTrip proves every admin-onboarding
// field survives the wire in both directions: the request's admin_* and
// trust_anchors_pem fields reach the server, and the 201 body's flat tenant
// fields plus "admin" (TenantAdminCredential) all decode back out --
// matching the actual server shape (pkg/api/rest: tenant fields flat,
// "admin" alongside), not transport.CreateTenantResponse's own nested JSON
// tags, which is exactly the kind of mismatch a hand-rolled decode shape
// (restTenant plus an inline Admin field) can introduce silently.
func TestRESTCreateTenant_AdminFieldsRoundTrip(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	var captured transport.CreateTenantRequest
	rs.handle(exactPath(http.MethodPost, "/v1/tenants"), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"id": "acme",
			"name": "Acme Corp",
			"isolation_type": "barrier",
			"createdAt": "2026-01-01T00:00:00Z",
			"updatedAt": "2026-01-02T00:00:00Z",
			"admin": {
				"username": "admin",
				"cert_pem": "admin-cert-pem",
				"serial": "admin-serial",
				"thumbprint": "admin-thumb",
				"awaits_barrier_init": false
			}
		}`))
	})
	resp, err := rt.CreateTenant(context.Background(), &transport.CreateTenantRequest{
		ID:              "acme",
		Name:            "Acme Corp",
		IsolationType:   transport.TenantIsolationBarrier,
		AdminUsername:   "admin",
		AdminCSRPEM:     "admin-csr-pem",
		TrustAnchorsPEM: "anchors-pem",
	})
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if captured.AdminUsername != "admin" || captured.AdminCSRPEM != "admin-csr-pem" || captured.TrustAnchorsPEM != "anchors-pem" {
		t.Fatalf("request captured = %+v", captured)
	}
	if resp.Tenant.ID != "acme" || resp.Tenant.Name != "Acme Corp" || resp.Tenant.IsolationType != transport.TenantIsolationBarrier {
		t.Fatalf("Tenant = %+v", resp.Tenant)
	}
	if resp.Admin.Username != "admin" || resp.Admin.CertPEM != "admin-cert-pem" ||
		resp.Admin.Serial != "admin-serial" || resp.Admin.Thumbprint != "admin-thumb" {
		t.Fatalf("Admin = %+v", resp.Admin)
	}
	if resp.Admin.AwaitsBarrierInit {
		t.Fatalf("AwaitsBarrierInit = true, want false")
	}
}

// TestRESTCreateTenant_AwaitsBarrierInit proves the dedicated-barrier
// onboarding path -- a zero UserID alongside AwaitsBarrierInit=true -- is
// not silently coerced to a different zero-value interpretation.
func TestRESTCreateTenant_AwaitsBarrierInit(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/tenants"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"id": "acme",
			"name": "Acme Corp",
			"admin": {
				"username": "admin",
				"cert_pem": "admin-cert-pem",
				"awaits_barrier_init": true
			}
		}`))
	})
	resp, err := rt.CreateTenant(context.Background(), &transport.CreateTenantRequest{
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

func TestRESTCreateTenant_ConflictMapsToTypedStatus(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/v1/tenants"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "tenant already exists", http.StatusConflict)
	})
	_, err := rt.CreateTenant(context.Background(), &transport.CreateTenantRequest{ID: "acme", Name: "Acme", AdminUsername: "admin"})
	if err == nil {
		t.Fatal("expected error")
	}
	var re *transport.RequestError
	if !errors.As(err, &re) || re.StatusCode != http.StatusConflict {
		t.Fatalf("err = %v (%T); want a *transport.RequestError carrying 409", err, err)
	}
}

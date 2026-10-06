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
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
)

// dispatchFull drives a request through the FULL chi router (the handler's
// {id} and {fingerprint} path parameters are chi route params, not legacy
// dispatch-map entries), matching how certs_tenant_scope_test.go exercises
// these same routes.
func dispatchFull(srv *Server, method, path string, body string) *httptest.ResponseRecorder {
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rdr)
	rec := httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rec, req)
	return rec
}

// ---------------------------------------------------------------------------
// POST /v1/tenants/{id}/trust-anchors (TrustAnchorAdd)
// ---------------------------------------------------------------------------

func TestHandleAddTrustAnchors_Success(t *testing.T) {
	addedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ce := &fakeCertEnroll{
		addFn: func(_ context.Context, tenantID string, bundlePEM []byte) ([]trustanchor.Anchor, error) {
			if tenantID != "acme" {
				t.Fatalf("expected tenant 'acme', got %q", tenantID)
			}
			if string(bundlePEM) != "ca-pem" {
				t.Fatalf("expected bundlePEM 'ca-pem', got %q", string(bundlePEM))
			}
			return []trustanchor.Anchor{{
				Fingerprint: "fp-1",
				Subject:     "CN=acme-root",
				CertPEM:     []byte("ca-pem"),
				AddedAt:     addedAt,
			}}, nil
		},
	}
	srv := newEnrollAuthServer(ce)

	rec := dispatchFull(srv, http.MethodPost, "/v1/tenants/acme/trust-anchors", `{"certs_pem":"ca-pem"}`)

	assertStatus(t, rec, http.StatusCreated)
	var out trustAnchorsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Anchors) != 1 {
		t.Fatalf("expected 1 anchor, got %d", len(out.Anchors))
	}
	if out.Anchors[0].Fingerprint != "fp-1" {
		t.Errorf("expected fingerprint 'fp-1', got %q", out.Anchors[0].Fingerprint)
	}
	if out.Anchors[0].Subject != "CN=acme-root" {
		t.Errorf("expected subject 'CN=acme-root', got %q", out.Anchors[0].Subject)
	}
}

func TestHandleAddTrustAnchors_ServiceError(t *testing.T) {
	ce := &fakeCertEnroll{
		addFn: func(context.Context, string, []byte) ([]trustanchor.Anchor, error) {
			return nil, &trustanchor.EmptyBundleError{}
		},
	}
	srv := newEnrollAuthServer(ce)

	rec := dispatchFull(srv, http.MethodPost, "/v1/tenants/acme/trust-anchors", `{"certs_pem":""}`)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleAddTrustAnchors_RouteNotRegisteredWithoutEnrollment(t *testing.T) {
	srv := newAuthServer(nil, nil, nil)

	rec := dispatchFull(srv, http.MethodPost, "/v1/tenants/acme/trust-anchors", `{"certs_pem":"ca-pem"}`)

	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// GET /v1/tenants/{id}/trust-anchors (TrustAnchorList)
// ---------------------------------------------------------------------------

func TestHandleListTrustAnchors_Success(t *testing.T) {
	ce := &fakeCertEnroll{
		listFn: func(_ context.Context, tenantID string) ([]trustanchor.Anchor, error) {
			if tenantID != "acme" {
				t.Fatalf("expected tenant 'acme', got %q", tenantID)
			}
			return []trustanchor.Anchor{{Fingerprint: "fp-1"}, {Fingerprint: "fp-2"}}, nil
		},
	}
	srv := newEnrollAuthServer(ce)

	rec := dispatchFull(srv, http.MethodGet, "/v1/tenants/acme/trust-anchors", "")

	assertStatus(t, rec, http.StatusOK)
	var out trustAnchorsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Anchors) != 2 {
		t.Fatalf("expected 2 anchors, got %d", len(out.Anchors))
	}
}

func TestHandleListTrustAnchors_ServiceError(t *testing.T) {
	ce := &fakeCertEnroll{
		listFn: func(context.Context, string) ([]trustanchor.Anchor, error) {
			return nil, errSentinel
		},
	}
	srv := newEnrollAuthServer(ce)

	rec := dispatchFull(srv, http.MethodGet, "/v1/tenants/acme/trust-anchors", "")

	assertStatus(t, rec, http.StatusInternalServerError)
}

// ---------------------------------------------------------------------------
// DELETE /v1/tenants/{id}/trust-anchors/{fingerprint} (TrustAnchorRemove)
// ---------------------------------------------------------------------------

func TestHandleRemoveTrustAnchor_Success(t *testing.T) {
	var gotTenant, gotFingerprint string
	ce := &fakeCertEnroll{
		removeFn: func(_ context.Context, tenantID, fingerprint string) error {
			gotTenant, gotFingerprint = tenantID, fingerprint
			return nil
		},
	}
	srv := newEnrollAuthServer(ce)

	rec := dispatchFull(srv, http.MethodDelete, "/v1/tenants/acme/trust-anchors/fp-1", "")

	assertStatus(t, rec, http.StatusNoContent)
	if gotTenant != "acme" || gotFingerprint != "fp-1" {
		t.Errorf("RemoveTrustAnchor(%q, %q), want (acme, fp-1)", gotTenant, gotFingerprint)
	}
}

func TestHandleRemoveTrustAnchor_ServiceError(t *testing.T) {
	ce := &fakeCertEnroll{
		removeFn: func(context.Context, string, string) error {
			return &trustanchor.AnchorNotFoundError{Fingerprint: "fp-missing"}
		},
	}
	srv := newEnrollAuthServer(ce)

	rec := dispatchFull(srv, http.MethodDelete, "/v1/tenants/acme/trust-anchors/fp-missing", "")

	assertStatus(t, rec, http.StatusNotFound)
}

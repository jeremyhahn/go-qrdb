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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestOfficerRegister_CSRPEMTravelsAsPlainString is the regression guard for
// bug 1 (see test/integration/officer_ceremony_cli_test.go): the REST wire
// contract for officer CSR registration is a plain PEM string, not the
// base64 encoding encoding/json would produce for a []byte field. This test
// pins the raw bytes actually placed on the wire by OfficerRegister and
// asserts they equal the PEM text verbatim, with no base64 layer.
func TestOfficerRegister_CSRPEMTravelsAsPlainString(t *testing.T) {
	const csrPEM = "-----BEGIN CERTIFICATE REQUEST-----\nMIIBozCCAUgCAQAw\n-----END CERTIFICATE REQUEST-----\n"

	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"cert_pem":    "-----BEGIN CERTIFICATE-----\nZm9v\n-----END CERTIFICATE-----\n",
			"share_index": 1,
		})
	}))
	defer srv.Close()

	tr := newWiredTransport(srv)
	resp, err := tr.OfficerRegister(t.Context(), &transport.OfficerRegisterRequest{
		Username: "officer-1",
		CSRPEM:   []byte(csrPEM),
	})
	if err != nil {
		t.Fatalf("OfficerRegister: %v", err)
	}

	gotCSR, ok := gotBody["csr_pem"].(string)
	if !ok {
		t.Fatalf("csr_pem not a string in wire body: %#v", gotBody["csr_pem"])
	}
	if gotCSR != csrPEM {
		t.Errorf("csr_pem on the wire = %q, want the plain PEM text %q (must not be base64-encoded)", gotCSR, csrPEM)
	}

	// The response cert_pem is also a plain PEM string on the wire; it must
	// be decoded back into transport.OfficerRegisterResponse.CertPEM ([]byte)
	// without going through base64.
	wantCert := "-----BEGIN CERTIFICATE-----\nZm9v\n-----END CERTIFICATE-----\n"
	if string(resp.CertPEM) != wantCert {
		t.Errorf("CertPEM = %q, want %q", resp.CertPEM, wantCert)
	}
	if resp.ShareIndex != 1 {
		t.Errorf("ShareIndex = %d, want 1", resp.ShareIndex)
	}
}

// TestRecoveryOfficerRegister_CSRPEMTravelsAsPlainString proves the same
// plain-string wire contract holds for the recovery-officer registration
// endpoint.
func TestRecoveryOfficerRegister_CSRPEMTravelsAsPlainString(t *testing.T) {
	const csrPEM = "-----BEGIN CERTIFICATE REQUEST-----\nrecovery-csr\n-----END CERTIFICATE REQUEST-----\n"

	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"cert_pem":    "-----BEGIN CERTIFICATE-----\nrecovery-cert\n-----END CERTIFICATE-----\n",
			"share_index": 2,
		})
	}))
	defer srv.Close()

	tr := newWiredTransport(srv)
	resp, err := tr.RecoveryOfficerRegister(t.Context(), &transport.OfficerRegisterRequest{
		Username: "recovery-officer-1",
		CSRPEM:   []byte(csrPEM),
	})
	if err != nil {
		t.Fatalf("RecoveryOfficerRegister: %v", err)
	}

	if gotBody["csr_pem"] != csrPEM {
		t.Errorf("csr_pem on the wire = %v, want the plain PEM text %q", gotBody["csr_pem"], csrPEM)
	}
	if string(resp.CertPEM) != "-----BEGIN CERTIFICATE-----\nrecovery-cert\n-----END CERTIFICATE-----\n" {
		t.Errorf("CertPEM = %q", resp.CertPEM)
	}
	if resp.ShareIndex != 2 {
		t.Errorf("ShareIndex = %d, want 2", resp.ShareIndex)
	}
}

// TestTenantBarrierOfficerRegister_CSRPEMTravelsAsPlainString proves the
// per-tenant officer registration endpoint uses the same plain-string wire
// contract as the system/recovery variants, including AuthenticatorRef.
func TestTenantBarrierOfficerRegister_CSRPEMTravelsAsPlainString(t *testing.T) {
	const csrPEM = "-----BEGIN CERTIFICATE REQUEST-----\ntenant-csr\n-----END CERTIFICATE REQUEST-----\n"

	var gotBody map[string]any
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"cert_pem":    "-----BEGIN CERTIFICATE-----\ntenant-cert\n-----END CERTIFICATE-----\n",
			"share_index": 3,
		})
	}))
	defer srv.Close()

	tr := newWiredTransport(srv)
	resp, err := tr.TenantBarrierOfficerRegister(t.Context(), &transport.TenantOfficerRegisterRequest{
		TenantID:         "tenant-a",
		Username:         "officer-1",
		CSRPEM:           []byte(csrPEM),
		AuthenticatorRef: "slot-9a",
	})
	if err != nil {
		t.Fatalf("TenantBarrierOfficerRegister: %v", err)
	}

	if gotPath != "/v1/tenants/tenant-a/barrier/officers" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["csr_pem"] != csrPEM {
		t.Errorf("csr_pem on the wire = %v, want the plain PEM text %q", gotBody["csr_pem"], csrPEM)
	}
	if gotBody["authenticator_ref"] != "slot-9a" {
		t.Errorf("authenticator_ref on the wire = %v, want %q", gotBody["authenticator_ref"], "slot-9a")
	}
	// tenant_id must not be duplicated into the body; it belongs in the path.
	if _, ok := gotBody["tenant_id"]; ok {
		t.Errorf("tenant_id must not appear in the request body, got %v", gotBody["tenant_id"])
	}
	if string(resp.CertPEM) != "-----BEGIN CERTIFICATE-----\ntenant-cert\n-----END CERTIFICATE-----\n" {
		t.Errorf("CertPEM = %q", resp.CertPEM)
	}
	if resp.ShareIndex != 3 {
		t.Errorf("ShareIndex = %d, want 3", resp.ShareIndex)
	}
}

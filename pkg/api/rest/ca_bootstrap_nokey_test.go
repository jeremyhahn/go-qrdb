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
	"bytes"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
)

// assertNoPrivateKeyPEM fails when body carries any PEM block whose type
// names private-key material, in any encoding the stack produces (EC,
// PKCS#8, RSA). It walks every block rather than searching for one string so
// a key hidden behind a certificate is still found.
func assertNoPrivateKeyPEM(t *testing.T, body []byte) {
	t.Helper()
	if bytes.Contains(body, []byte("PRIVATE KEY")) {
		t.Fatalf("response body carries private-key material:\n%s", body)
	}
	rest := body
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return
		}
		if strings.Contains(block.Type, "PRIVATE KEY") {
			t.Fatalf("response body carries a %s block", block.Type)
		}
	}
}

// TestCABootstrap_RawBodyNeverCarriesTheCAPrivateKey pins the security
// property of GET /v1/ca/bootstrap: it is a public route, reached before the
// caller holds any credential, so whatever it writes is disclosed to anyone
// who can reach the admin port. It must serve the CA certificate and nothing
// else. The assertion is on the raw HTTP body, the bytes a SPKI, DANE or
// Direct bootstrap client receives.
func TestCABootstrap_RawBodyNeverCarriesTheCAPrivateKey(t *testing.T) {
	caCertPEM, caKeyPEM, _, _, err := clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	if !bytes.Contains(caKeyPEM, []byte("PRIVATE KEY")) {
		t.Fatal("fixture: the generated CA key is not PEM private-key material")
	}

	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithCABootstrap(caCertPEM),
	)

	req := httptest.NewRequest(http.MethodGet, "/v1/ca/bootstrap", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/ca/bootstrap status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.Bytes()
	assertNoPrivateKeyPEM(t, body)
	if !bytes.Equal(body, caCertPEM) {
		t.Fatalf("body = %q, want exactly the CA certificate", body)
	}
}

// TestCABootstrap_StripsKeyMaterialHandedInAsTheCertificate covers the
// misuse the option's signature cannot rule out: a caller concatenating the
// CA key onto the certificate PEM (the old bundle shape). The endpoint serves
// the CERTIFICATE blocks and drops everything else.
func TestCABootstrap_StripsKeyMaterialHandedInAsTheCertificate(t *testing.T) {
	caCertPEM, caKeyPEM, _, _, err := clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	bundle := append(append([]byte{}, caCertPEM...), caKeyPEM...)

	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithCABootstrap(bundle),
	)

	req := httptest.NewRequest(http.MethodGet, "/v1/ca/bootstrap", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/ca/bootstrap status = %d, want %d", rec.Code, http.StatusOK)
	}
	assertNoPrivateKeyPEM(t, rec.Body.Bytes())
	if !bytes.Equal(rec.Body.Bytes(), caCertPEM) {
		t.Fatalf("body = %q, want exactly the CA certificate", rec.Body.Bytes())
	}
}

// TestCABootstrap_KeyOnlyInputRegistersNoRoute: input with no certificate at
// all leaves nothing to serve, so the route is not registered -- the
// endpoint must never answer with the key and must not answer empty either.
func TestCABootstrap_KeyOnlyInputRegistersNoRoute(t *testing.T) {
	_, caKeyPEM, _, _, err := clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithCABootstrap(caKeyPEM),
	)

	req := httptest.NewRequest(http.MethodGet, "/v1/ca/bootstrap", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /v1/ca/bootstrap status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	assertNoPrivateKeyPEM(t, rec.Body.Bytes())
}

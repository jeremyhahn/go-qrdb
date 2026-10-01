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
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/sotest"
)

// ---------------------------------------------------------------------------
// The SO PIN travels only over TLS, and only to the configured host.
//
// Each server records every X-QRDB-SO-PIN header that reaches it, so a test
// can assert that one never did.
// ---------------------------------------------------------------------------

const soTLSTestPIN = "correct horse battery staple"

type pinRecorder struct {
	mu   sync.Mutex
	pins []string
	hits int
}

func (r *pinRecorder) record(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hits++
	if pin := req.Header.Get(transport.SOPINHeader); pin != "" {
		r.pins = append(r.pins, pin)
	}
}

func (r *pinRecorder) sawPIN() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pins) > 0
}

// pinServer answers /health and records then serves /v1/barrier/rekey.
func pinServer(rec *pinRecorder) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/v1/barrier/rekey", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"threshold":2,"total_shares":3}`))
	})
	return mux
}

// redirectingServer answers /health and redirects /v1/barrier/rekey to target.
func redirectingServer(rec *pinRecorder, target string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/v1/barrier/rekey", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		http.Redirect(w, r, target+"/v1/barrier/rekey", http.StatusTemporaryRedirect)
	})
	return mux
}

func connectREST(t *testing.T, address string, tlsCfg *tls.Config) *Transport {
	t.Helper()
	opts := []transport.Option{transport.WithAddress(address), transport.WithTimeout(5 * time.Second)}
	if tlsCfg != nil {
		opts = append(opts, transport.WithTLSConfig(tlsCfg))
	}
	tr, err := New(opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

func rekeyWithPIN(t *testing.T, tr *Transport) error {
	t.Helper()
	ctx, release := sotest.BindPIN(t, soTLSTestPIN)
	defer release()
	_, err := tr.BarrierRekey(ctx, &transport.BarrierRekeyRequest{Threshold: 2, Total: 3})
	return err
}

// trusting returns a client TLS config that trusts srv's certificate.
func trusting(srv *httptest.Server) *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

func TestSOPIN_PlaintextConnectionNeverCarriesThePIN(t *testing.T) {
	rec := &pinRecorder{}
	srv := httptest.NewServer(pinServer(rec))
	defer srv.Close()

	err := rekeyWithPIN(t, connectREST(t, srv.URL, nil))
	if rec.sawPIN() {
		t.Fatal("the SO PIN crossed a plaintext connection")
	}
	var tlsErr *transport.SOPINRequiresTLSError
	if !errors.As(err, &tlsErr) {
		t.Fatalf("err = %v, want *transport.SOPINRequiresTLSError", err)
	}
}

func TestSOPIN_RedirectToAnotherHostNeverCarriesThePIN(t *testing.T) {
	foreignRec := &pinRecorder{}
	foreign := httptest.NewTLSServer(pinServer(foreignRec))
	defer foreign.Close()
	originRec := &pinRecorder{}
	origin := httptest.NewTLSServer(redirectingServer(originRec, foreign.URL))
	defer origin.Close()

	// One trust store for both: httptest servers share a certificate.
	err := rekeyWithPIN(t, connectREST(t, origin.URL, trusting(origin)))
	if foreignRec.sawPIN() {
		t.Fatal("the SO PIN followed a redirect to another host")
	}
	if !originRec.sawPIN() {
		t.Fatal("the configured host did not receive the PIN; the test proves nothing")
	}
	var hostErr *transport.SOPINForeignHostError
	if !errors.As(err, &hostErr) {
		t.Fatalf("err = %v, want *transport.SOPINForeignHostError", err)
	}
}

func TestSOPIN_RedirectToHTTPNeverCarriesThePIN(t *testing.T) {
	plainRec := &pinRecorder{}
	plain := httptest.NewServer(pinServer(plainRec))
	defer plain.Close()
	originRec := &pinRecorder{}
	origin := httptest.NewTLSServer(redirectingServer(originRec, plain.URL))
	defer origin.Close()

	err := rekeyWithPIN(t, connectREST(t, origin.URL, trusting(origin)))
	if plainRec.sawPIN() {
		t.Fatal("the SO PIN followed a redirect to http")
	}
	var tlsErr *transport.SOPINRequiresTLSError
	var hostErr *transport.SOPINForeignHostError
	if !errors.As(err, &tlsErr) && !errors.As(err, &hostErr) {
		t.Fatalf("err = %v, want the redirect refused", err)
	}
}

func TestSOPIN_ConfiguredTLSHostCarriesThePIN(t *testing.T) {
	rec := &pinRecorder{}
	srv := httptest.NewTLSServer(pinServer(rec))
	defer srv.Close()
	if err := rekeyWithPIN(t, connectREST(t, srv.URL, trusting(srv))); err != nil {
		t.Fatalf("rekey over TLS: %v", err)
	}
	if !rec.sawPIN() {
		t.Fatal("the configured TLS host did not receive the PIN")
	}
}

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
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony/ceremonytest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// restDBHolder is one database share holder's client side over REST.
type restDBHolder struct {
	name     string
	key      *ecdsa.PrivateKey
	csr      string
	cert     *x509.Certificate
	envelope []byte
}

func newRESTDBHolder(t *testing.T, name string) *restDBHolder {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "ignored"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return &restDBHolder{name: name, key: key, csr: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))}
}

// newDBHolderServer is a REST server over a real database service whose
// "vault" database is a 2-of-2 Shamir database on a dedicated store.
func newDBHolderServer(t *testing.T) *Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := database.NewManager(logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	systemStore := seal.NewMemoryBackend()
	system, err := seal.NewStorageBarrier(logger, systemStore, seal.BarrierConfig{RootKeyPath: "root-key"}, seal.NewNoneStrategy())
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatal(err)
	}
	bm, err := database.NewDatabaseBarrierManager(logger, system)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := ceremonytest.NewCA(t, "Database Holder CA")
	bm.SetHolderDeps(ceremony.HolderDeps{CA: ca, TrustDomain: authtest.ClientTrustDomain})
	mgr.SetBarrierManager(bm)
	cfg := database.DatabaseConfig{
		Name: "vault", Backend: database.BackendMemory, DataDir: t.TempDir(),
		Barrier: &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModeShamir, SharesRequired: 2, SharesTotal: 2},
	}
	if _, err := mgr.CreateWithCredentials(context.Background(), cfg, seal.Credentials{}); err != nil {
		t.Fatal(err)
	}
	svc := service.NewDatabaseService(mgr, service.WithBarrierManager(bm))
	return NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithDatabaseService(svc))
}

// postJSON dispatches a POST with body and, when cert is set, a TLS
// connection presenting it.
func postJSON(t *testing.T, srv *Server, path string, body any, cert *x509.Certificate) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	if cert != nil {
		req.TLS = &tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{cert},
			VerifiedChains:   [][]*x509.Certificate{{cert}},
		}
	}
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	return rec
}

// decodeInto decodes rec's body into v, failing on a non-200 status.
func decodeInto(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatal(err)
	}
}

// submitREST answers the mtls factor and submits h's share to op.
func submitREST(t *testing.T, srv *Server, op string, h *restDBHolder) *transport.QuorumOutcome {
	t.Helper()
	var ch transport.QuorumChallenge
	decodeInto(t, postJSON(t, srv, "/v1/databases/vault/barrier/quorum/begin", transport.QuorumBeginRequest{Op: op, Username: h.name}, nil), &ch)
	sig, err := mtls.SignChallenge(h.key, ch.ChallengeID, ch.MethodData[mtls.MethodDataPurpose], ch.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	env, err := ceremony.ParseShareEnvelope(h.envelope)
	if err != nil {
		t.Fatal(err)
	}
	share, err := env.OpenWithKey(h.key)
	if err != nil {
		t.Fatal(err)
	}
	var out transport.QuorumOutcome
	decodeInto(t, postJSON(t, srv, "/v1/databases/vault/barrier/quorum/submit", transport.QuorumSubmitRequest{
		Op: op, Username: h.name, ChallengeID: ch.ChallengeID, Factor: ch.Factor, Response: sig, Share: share,
	}, nil), &out)
	return &out
}

// TestDatabaseHolders_REST drives a Shamir database's custody over REST:
// enrolment, listing, initialization, claims over the holder's certificate,
// a quorum seal and unseal, and a rekey round.
func TestDatabaseHolders_REST(t *testing.T) {
	srv := newDBHolderServer(t)
	holders := []*restDBHolder{newRESTDBHolder(t, "alice"), newRESTDBHolder(t, "bob")}
	for _, h := range holders {
		var resp transport.OfficerRegisterResponse
		decodeInto(t, postJSON(t, srv, "/v1/databases/vault/barrier/officers", transport.OfficerRegisterRequest{Username: h.name, CSRPEM: h.csr}, nil), &resp)
		cert, err := mtls.ParseCertificatePEM([]byte(resp.CertPEM))
		if err != nil {
			t.Fatal(err)
		}
		h.cert = cert
	}

	listRec := httptest.NewRecorder()
	srv.dispatch(listRec, httptest.NewRequest(http.MethodGet, "/v1/databases/vault/barrier/officers", nil))
	var list struct {
		Officers []transport.OfficerStatus `json:"officers"`
	}
	decodeInto(t, listRec, &list)
	if len(list.Officers) != 2 {
		t.Fatalf("listed %d holders, want 2", len(list.Officers))
	}

	var init transport.BarrierInitializeShamirResponse
	decodeInto(t, postJSON(t, srv, "/v1/databases/vault/barrier/shamir/initialize", map[string]string{}, nil), &init)
	if init.Threshold != 2 || init.TotalShares != 2 || init.ShareCount != 2 {
		t.Fatalf("initialize = %+v", init)
	}

	for _, h := range holders {
		if rec := postJSON(t, srv, "/v1/databases/vault/barrier/officers/"+h.name+"/claim/begin", struct{}{}, nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("claim without a certificate = %d, want 401", rec.Code)
		}
		var begin transport.ShareClaimBeginResponse
		decodeInto(t, postJSON(t, srv, "/v1/databases/vault/barrier/officers/"+h.name+"/claim/begin", struct{}{}, h.cert), &begin)
		nonce, err := hex.DecodeString(begin.NonceHex)
		if err != nil {
			t.Fatal(err)
		}
		sig, err := ceremony.SignShareClaim(h.key, ceremony.DatabaseClaimLabel("vault"), h.name, nonce)
		if err != nil {
			t.Fatal(err)
		}
		var done transport.ShareClaimCompleteResponse
		decodeInto(t, postJSON(t, srv, "/v1/databases/vault/barrier/officers/"+h.name+"/claim/complete",
			transport.ShareClaimCompleteRequest{NonceHex: begin.NonceHex, Signature: sig}, h.cert), &done)
		h.envelope = done.Envelope
	}

	if rec := postJSON(t, srv, "/v1/databases/vault/seal", struct{}{}, nil); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("single-caller seal = %d, want 422", rec.Code)
	}
	submitREST(t, srv, "seal", holders[0])
	if out := submitREST(t, srv, "seal", holders[1]); !out.Complete {
		t.Fatalf("seal quorum = %+v", out)
	}
	submitREST(t, srv, "unseal", holders[1])
	if out := submitREST(t, srv, "unseal", holders[0]); !out.Complete {
		t.Fatalf("unseal quorum = %+v", out)
	}

	var round transport.QuorumRound
	decodeInto(t, postJSON(t, srv, "/v1/databases/vault/barrier/rekey/start", map[string]int{"threshold": 2}, nil), &round)
	if round.Op != "rekey" {
		t.Fatalf("rekey round = %+v", round)
	}
	if rec := postJSON(t, srv, "/v1/databases/vault/barrier/officers/revoke", map[string]any{"username": "bob", "threshold": 2}, nil); rec.Code != http.StatusConflict {
		t.Fatalf("revoking below the threshold = %d, want 409; body %s", rec.Code, rec.Body.String())
	}
}

// TestDatabaseHolders_RESTRefusals covers the routes' own refusals: a
// database with no Shamir barrier, a malformed body, and a server without a
// holder-capable database service.
func TestDatabaseHolders_RESTRefusals(t *testing.T) {
	srv := newDBHolderServer(t)
	if rec := postJSON(t, srv, "/v1/databases/absent/barrier/officers", transport.OfficerRegisterRequest{Username: "a"}, nil); rec.Code == http.StatusOK {
		t.Fatal("enrolling on an absent database succeeded")
	}
	bad := httptest.NewRequest(http.MethodPost, "/v1/databases/vault/barrier/quorum/begin", bytes.NewReader([]byte("{")))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, bad)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body = %d, want 400", rec.Code)
	}

	plain := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithDatabaseService(&mockDatabaseService{}))
	if rec := postJSON(t, plain, "/v1/databases/vault/barrier/rekey/start", map[string]int{"threshold": 2}, nil); rec.Code != http.StatusNotImplemented {
		t.Fatalf("a database service without holder custody = %d, want 501", rec.Code)
	}
	if rec := postJSON(t, srv, "/v1/databases/vault/barrier/officers/a/b/claim/begin", struct{}{}, nil); rec.Code == http.StatusOK {
		t.Fatal("a nested username path was served")
	}
}

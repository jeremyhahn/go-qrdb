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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	quicraftseal "github.com/jeremyhahn/go-quicraft/pkg/seal"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// extMockBarrierService — wraps mockBarrierService with configurable SO fns
// ---------------------------------------------------------------------------

type extMockBarrierService struct {
	*mockBarrierService
	officerRegisterFn         func(ctx context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error)
	officerListFn             func(ctx context.Context) ([]*service.BarrierOfficerStatus, error)
	officerClaimStatusFn      func(ctx context.Context) ([]*service.BarrierOfficerStatus, error)
	shareClaimBeginFn         func(ctx context.Context, username, thumbprint string) (*service.BarrierShareClaimChallenge, error)
	shareClaimCompleteFn      func(ctx context.Context, username, nonceHex string, sig []byte, thumbprint string) (*service.BarrierShareClaimResult, error)
	shamirSealVoteFn          func(ctx context.Context, share string) (*service.UnsealShareProgress, error)
	recoveryOfficerRegisterFn func(ctx context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error)
	recoveryClaimBeginFn      func(ctx context.Context, username, thumbprint string) (*service.BarrierShareClaimChallenge, error)
	recoveryClaimCompleteFn   func(ctx context.Context, username, nonceHex string, sig []byte, thumbprint string) (*service.BarrierShareClaimResult, error)
}

func newExtBarrierSvc() *extMockBarrierService {
	return &extMockBarrierService{mockBarrierService: &mockBarrierService{}}
}

var _ service.BarrierServicer = (*extMockBarrierService)(nil)

func (e *extMockBarrierService) BarrierOfficerRegister(ctx context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	if e.officerRegisterFn != nil {
		return e.officerRegisterFn(ctx, req)
	}
	return e.mockBarrierService.BarrierOfficerRegister(ctx, req)
}

func (e *extMockBarrierService) BarrierOfficerList(ctx context.Context) ([]*service.BarrierOfficerStatus, error) {
	if e.officerListFn != nil {
		return e.officerListFn(ctx)
	}
	return e.mockBarrierService.BarrierOfficerList(ctx)
}

func (e *extMockBarrierService) BarrierOfficerClaimStatus(ctx context.Context) ([]*service.BarrierOfficerStatus, error) {
	if e.officerClaimStatusFn != nil {
		return e.officerClaimStatusFn(ctx)
	}
	return e.mockBarrierService.BarrierOfficerClaimStatus(ctx)
}

func (e *extMockBarrierService) BarrierShareClaimBegin(ctx context.Context, username, thumbprint string) (*service.BarrierShareClaimChallenge, error) {
	if e.shareClaimBeginFn != nil {
		return e.shareClaimBeginFn(ctx, username, thumbprint)
	}
	return e.mockBarrierService.BarrierShareClaimBegin(ctx, username, thumbprint)
}

func (e *extMockBarrierService) BarrierShareClaimComplete(ctx context.Context, username, nonceHex string, sig []byte, thumbprint string) (*service.BarrierShareClaimResult, error) {
	if e.shareClaimCompleteFn != nil {
		return e.shareClaimCompleteFn(ctx, username, nonceHex, sig, thumbprint)
	}
	return e.mockBarrierService.BarrierShareClaimComplete(ctx, username, nonceHex, sig, thumbprint)
}

func (e *extMockBarrierService) BarrierShamirSealVote(ctx context.Context, share string) (*service.UnsealShareProgress, error) {
	if e.shamirSealVoteFn != nil {
		return e.shamirSealVoteFn(ctx, share)
	}
	return e.mockBarrierService.BarrierShamirSealVote(ctx, share)
}

func (e *extMockBarrierService) BarrierRecoveryOfficerRegister(ctx context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	if e.recoveryOfficerRegisterFn != nil {
		return e.recoveryOfficerRegisterFn(ctx, req)
	}
	return e.mockBarrierService.BarrierRecoveryOfficerRegister(ctx, req)
}

func (e *extMockBarrierService) BarrierRecoveryShareClaimBegin(ctx context.Context, username, thumbprint string) (*service.BarrierShareClaimChallenge, error) {
	if e.recoveryClaimBeginFn != nil {
		return e.recoveryClaimBeginFn(ctx, username, thumbprint)
	}
	return e.mockBarrierService.BarrierRecoveryShareClaimBegin(ctx, username, thumbprint)
}

func (e *extMockBarrierService) BarrierRecoveryShareClaimComplete(ctx context.Context, username, nonceHex string, sig []byte, thumbprint string) (*service.BarrierShareClaimResult, error) {
	if e.recoveryClaimCompleteFn != nil {
		return e.recoveryClaimCompleteFn(ctx, username, nonceHex, sig, thumbprint)
	}
	return e.mockBarrierService.BarrierRecoveryShareClaimComplete(ctx, username, nonceHex, sig, thumbprint)
}

// ---------------------------------------------------------------------------
// injectFakeTLS — helper: sets r.TLS with a self-signed cert so that
// restCertThumbprint can compute a thumbprint without returning an error.
// ---------------------------------------------------------------------------

func injectFakeTLS(r *http.Request) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic("injectFakeTLS: generate key: " + err.Error())
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-officer"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic("injectFakeTLS: create cert: " + err.Error())
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		panic("injectFakeTLS: parse cert: " + err.Error())
	}
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
}

// fakePEMCert returns a PEM-encoded self-signed certificate for use in
// register request bodies.
func fakePEMCert() string {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic("fakePEMCert: " + err.Error())
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "fake-cert"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic("fakePEMCert: create: " + err.Error())
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// ---------------------------------------------------------------------------
// mapHTTPError — exhaustive table test
// ---------------------------------------------------------------------------

func TestMapBarrierCeremonyHTTPError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{"CertThumbprintMismatch", ceremony.ErrCertThumbprintMismatch, http.StatusForbidden},
		{"OfficerNotFound", ceremony.ErrOfficerNotFound, http.StatusNotFound},
		{"ShareAlreadyClaimed", ceremony.ErrShareAlreadyClaimed, http.StatusConflict},
		{"NoPendingShare", ceremony.ErrNoPendingShare, http.StatusNotFound},
		{"ShareExpired", ceremony.ErrShareExpired, http.StatusGone},
		{"InsufficientOfficers", ceremony.ErrInsufficientOfficers, http.StatusUnprocessableEntity},
		{"ShamirBarrierRequiresQuorum", seal.ErrShamirBarrierRequiresQuorum, http.StatusUnprocessableEntity},
		{"SealVoteVerificationFailed", seal.ErrSealVoteVerificationFailed, http.StatusForbidden},
		{"UnsealKeyMismatch", seal.ErrUnsealKeyMismatch, http.StatusForbidden},
		{"DecryptionFailed", seal.ErrDecryptionFailed, http.StatusForbidden},
		{"WrappedUnsealDecryption", &quicraftseal.UnsealError{Strategy: "passphrase", Err: quicraftseal.ErrDecryptionFailed}, http.StatusForbidden},
		{"WrappedRootKeyCorrupt", seal.ErrWrappedRootKeyCorrupt, http.StatusInternalServerError},
		{"AlreadyUnsealed", seal.ErrAlreadyUnsealed, http.StatusConflict},
		{"AlreadyInitialized", seal.ErrAlreadyInitialized, http.StatusConflict},
		{"NotInitialized", seal.ErrNotInitialized, http.StatusConflict},
		{"InvalidCredentials", seal.ErrInvalidCredentials, http.StatusForbidden},
		{"CeremonyNotConfigured", service.ErrCeremonyNotConfigured, http.StatusUnprocessableEntity},
		{"GenericError", errSentinel, http.StatusInternalServerError},
		{"ServiceErrorFallthrough", &service.KeyNotFoundError{Key: "k"}, http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mapHTTPError(tc.err)
			if got != tc.wantCode {
				t.Fatalf("mapHTTPError(%v) = %d, want %d", tc.err, got, tc.wantCode)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// barrierOfficerStatusesToResponse — zero / non-zero ClaimedAt paths
// ---------------------------------------------------------------------------

func TestBarrierOfficerStatusesToResponse_Fields(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	officers := []*service.BarrierOfficerStatus{
		{
			Username:       "alice",
			CertThumbprint: "abc123",
			ShareIndex:     1,
			AuthFactor:     "software",
			SharePending:   true,
			EnrolledAt:     now,
			ClaimedAt:      time.Time{}, // zero — must NOT appear
		},
		{
			Username:       "bob",
			CertThumbprint: "def456",
			ShareIndex:     2,
			AuthFactor:     "fido2",
			SharePending:   false,
			EnrolledAt:     now,
			ClaimedAt:      now, // non-zero — must appear
		},
	}

	resp := barrierOfficerStatusesToResponse(officers)
	if len(resp) != 2 {
		t.Fatalf("len = %d, want 2", len(resp))
	}
	if _, ok := resp[0]["claimed_at"]; ok {
		t.Fatal("claimed_at present for unclaimed officer")
	}
	if resp[0]["username"] != "alice" {
		t.Fatalf("username = %v, want alice", resp[0]["username"])
	}
	if resp[0]["share_pending"] != true {
		t.Fatalf("share_pending = %v, want true", resp[0]["share_pending"])
	}
	if _, ok := resp[1]["claimed_at"]; !ok {
		t.Fatal("claimed_at missing for claimed officer")
	}
	if resp[1]["username"] != "bob" {
		t.Fatalf("username = %v, want bob", resp[1]["username"])
	}
}

// ---------------------------------------------------------------------------
// System barrier SO handlers
// ---------------------------------------------------------------------------

func TestHandleBarrierOfficerRegister_Success(t *testing.T) {
	svc := newExtBarrierSvc()
	svc.officerRegisterFn = func(_ context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
		if req.Username != "alice" {
			t.Fatalf("username = %q, want alice", req.Username)
		}
		return &service.BarrierOfficerRegisterResult{CertPEM: []byte("signed-cert"), ShareIndex: 1}, nil
	}
	srv := newBarrierTestServer(t, svc)

	certPEM := fakePEMCert()
	body := `{"username":"alice","csr_pem":"` + strings.ReplaceAll(certPEM, "\n", "\\n") + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/officers", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp map[string]interface{}
	decodeJSON(t, rec, &resp)
	if resp["cert_pem"] != "signed-cert" {
		t.Fatalf("cert_pem = %v, want signed-cert", resp["cert_pem"])
	}
}

func TestHandleBarrierOfficerRegister_MissingUsername(t *testing.T) {
	srv := newBarrierTestServer(t, newExtBarrierSvc())

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/officers",
		strings.NewReader(`{"csr_pem":"csr-data"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleBarrierOfficerRegister_MissingCSR(t *testing.T) {
	srv := newBarrierTestServer(t, newExtBarrierSvc())

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/officers",
		strings.NewReader(`{"username":"alice"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleBarrierOfficerRegister_BadJSON(t *testing.T) {
	srv := newBarrierTestServer(t, newExtBarrierSvc())

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/officers",
		strings.NewReader(`{not-json`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleBarrierOfficerRegister_ServiceError(t *testing.T) {
	svc := newExtBarrierSvc()
	svc.officerRegisterFn = func(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
		return nil, service.ErrCeremonyNotConfigured
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/officers",
		strings.NewReader(`{"username":"alice","csr_pem":"csr-data"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusUnprocessableEntity)
}

func TestHandleBarrierOfficerList_Success(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	svc := newExtBarrierSvc()
	svc.officerListFn = func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
		return []*service.BarrierOfficerStatus{
			{Username: "alice", ShareIndex: 1, EnrolledAt: now},
		}, nil
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/barrier/officers", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp map[string]interface{}
	decodeJSON(t, rec, &resp)
	officers, ok := resp["officers"].([]interface{})
	if !ok || len(officers) != 1 {
		t.Fatalf("expected 1 officer, got %v", resp["officers"])
	}
}

func TestHandleBarrierOfficerList_ServiceError(t *testing.T) {
	svc := newExtBarrierSvc()
	svc.officerListFn = func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
		return nil, ceremony.ErrInsufficientOfficers
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/barrier/officers", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusUnprocessableEntity)
}

func TestHandleBarrierOfficerClaimStatus_Success(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	svc := newExtBarrierSvc()
	svc.officerClaimStatusFn = func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
		return []*service.BarrierOfficerStatus{
			{Username: "bob", ShareIndex: 2, ClaimedAt: now},
		}, nil
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/barrier/officers/claim-status", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
}

func TestHandleBarrierOfficerClaimStatus_ServiceError(t *testing.T) {
	svc := newExtBarrierSvc()
	svc.officerClaimStatusFn = func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
		return nil, ceremony.ErrOfficerNotFound
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/barrier/officers/claim-status", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

// handleBarrierShareClaimBegin — missing peer cert path (no TLS on request).

func TestHandleBarrierShareClaimBegin_MissingCert(t *testing.T) {
	srv := newBarrierTestServer(t, newExtBarrierSvc())

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/officers/alice/claim/begin", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusUnauthorized)
}

func TestHandleBarrierShareClaimBegin_ServiceError(t *testing.T) {
	svc := newExtBarrierSvc()
	svc.shareClaimBeginFn = func(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
		return nil, ceremony.ErrOfficerNotFound
	}
	srv := &Server{barrierSvc: svc}

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	injectFakeTLS(req)
	rec := httptest.NewRecorder()
	srv.handleBarrierShareClaimBegin(rec, req, "alice")

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleBarrierShareClaimBegin_Success(t *testing.T) {
	expires := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	svc := newExtBarrierSvc()
	svc.shareClaimBeginFn = func(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
		return &service.BarrierShareClaimChallenge{NonceHex: "deadbeef01", ExpiresAt: expires}, nil
	}
	srv := &Server{barrierSvc: svc}

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	injectFakeTLS(req)
	rec := httptest.NewRecorder()
	srv.handleBarrierShareClaimBegin(rec, req, "alice")

	assertStatus(t, rec, http.StatusOK)
	var resp map[string]interface{}
	decodeJSON(t, rec, &resp)
	if resp["nonce_hex"] != "deadbeef01" {
		t.Fatalf("nonce_hex = %v, want deadbeef01", resp["nonce_hex"])
	}
}

// handleBarrierShareClaimComplete tests.

func TestHandleBarrierShareClaimComplete_MissingCert(t *testing.T) {
	srv := newBarrierTestServer(t, newExtBarrierSvc())

	sig := base64.StdEncoding.EncodeToString([]byte("sig"))
	body := `{"nonce_hex":"aabbcc","signature":"` + sig + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/officers/alice/claim/complete", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusUnauthorized)
}

func TestHandleBarrierShareClaimComplete_MissingNonce(t *testing.T) {
	srv := &Server{barrierSvc: newExtBarrierSvc()}

	sig := base64.StdEncoding.EncodeToString([]byte("sig"))
	body := `{"signature":"` + sig + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	injectFakeTLS(req)
	rec := httptest.NewRecorder()
	srv.handleBarrierShareClaimComplete(rec, req, "alice")

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleBarrierShareClaimComplete_MissingSignature(t *testing.T) {
	srv := &Server{barrierSvc: newExtBarrierSvc()}

	body := `{"nonce_hex":"aabbcc"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	injectFakeTLS(req)
	rec := httptest.NewRecorder()
	srv.handleBarrierShareClaimComplete(rec, req, "alice")

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleBarrierShareClaimComplete_BadBase64(t *testing.T) {
	srv := &Server{barrierSvc: newExtBarrierSvc()}

	body := `{"nonce_hex":"aabbcc","signature":"!!!not-base64!!!"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	injectFakeTLS(req)
	rec := httptest.NewRecorder()
	srv.handleBarrierShareClaimComplete(rec, req, "alice")

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleBarrierShareClaimComplete_ServiceError(t *testing.T) {
	svc := newExtBarrierSvc()
	svc.shareClaimCompleteFn = func(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
		return nil, ceremony.ErrCertThumbprintMismatch
	}
	srv := &Server{barrierSvc: svc}

	sig := base64.StdEncoding.EncodeToString([]byte("valid-sig"))
	body := `{"nonce_hex":"aabbcc","signature":"` + sig + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	injectFakeTLS(req)
	rec := httptest.NewRecorder()
	srv.handleBarrierShareClaimComplete(rec, req, "alice")

	assertStatus(t, rec, http.StatusForbidden)
}

func TestHandleBarrierShareClaimComplete_Success(t *testing.T) {
	shareBytes := []byte("shamir-share-bytes")
	svc := newExtBarrierSvc()
	svc.shareClaimCompleteFn = func(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
		return &service.BarrierShareClaimResult{ShareIndex: 1, ShareBytes: shareBytes}, nil
	}
	srv := &Server{barrierSvc: svc}

	sig := base64.StdEncoding.EncodeToString([]byte("valid-sig"))
	body := `{"nonce_hex":"aabbcc","signature":"` + sig + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	injectFakeTLS(req)
	rec := httptest.NewRecorder()
	srv.handleBarrierShareClaimComplete(rec, req, "alice")

	assertStatus(t, rec, http.StatusOK)
	// INV-11: share_bytes must appear exactly here in the claiming officer's response.
	var resp map[string]interface{}
	decodeJSON(t, rec, &resp)
	if _, ok := resp["share_bytes"]; !ok {
		t.Fatal("share_bytes missing from claim-complete response")
	}
}

// handleBarrierShamirSealVote tests.

func TestHandleBarrierShamirSealVote_MissingShare(t *testing.T) {
	srv := newBarrierTestServer(t, newExtBarrierSvc())

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/shamir/seal-vote",
		strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleBarrierShamirSealVote_Success(t *testing.T) {
	svc := newExtBarrierSvc()
	svc.shamirSealVoteFn = func(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
		return &service.UnsealShareProgress{Required: 3, Submitted: 1, Complete: false}, nil
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/shamir/seal-vote",
		strings.NewReader(`{"share":"abc123"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
}

func TestHandleBarrierShamirSealVote_ServiceError(t *testing.T) {
	svc := newExtBarrierSvc()
	svc.shamirSealVoteFn = func(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
		return nil, seal.ErrSealVoteVerificationFailed
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/shamir/seal-vote",
		strings.NewReader(`{"share":"abc123"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusForbidden)
}

// Recovery officer register tests.

func TestHandleBarrierRecoveryOfficerRegister_Success(t *testing.T) {
	svc := newExtBarrierSvc()
	svc.recoveryOfficerRegisterFn = func(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
		return &service.BarrierOfficerRegisterResult{CertPEM: []byte("recovery-cert"), ShareIndex: 2}, nil
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/recovery/officers",
		strings.NewReader(`{"username":"recov-officer","csr_pem":"csr-data"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
}

func TestHandleBarrierRecoveryOfficerRegister_MissingUsername(t *testing.T) {
	srv := newBarrierTestServer(t, newExtBarrierSvc())

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/recovery/officers",
		strings.NewReader(`{"csr_pem":"csr-data"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleBarrierRecoveryShareClaimBegin_MissingCert(t *testing.T) {
	srv := newBarrierTestServer(t, newExtBarrierSvc())

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/recovery/officers/alice/claim/begin", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusUnauthorized)
}

func TestHandleBarrierRecoveryShareClaimBegin_Success(t *testing.T) {
	expires := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	svc := newExtBarrierSvc()
	svc.recoveryClaimBeginFn = func(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
		return &service.BarrierShareClaimChallenge{NonceHex: "cc00ff", ExpiresAt: expires}, nil
	}
	srv := &Server{barrierSvc: svc}

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	injectFakeTLS(req)
	rec := httptest.NewRecorder()
	srv.handleBarrierRecoveryShareClaimBegin(rec, req, "alice")

	assertStatus(t, rec, http.StatusOK)
}

func TestHandleBarrierRecoveryShareClaimComplete_MissingCert(t *testing.T) {
	srv := newBarrierTestServer(t, newExtBarrierSvc())

	sig := base64.StdEncoding.EncodeToString([]byte("sig"))
	body := `{"nonce_hex":"aabb","signature":"` + sig + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/recovery/officers/alice/claim/complete", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusUnauthorized)
}

func TestHandleBarrierRecoveryShareClaimComplete_Success(t *testing.T) {
	shareBytes := []byte("recovery-share-bytes")
	svc := newExtBarrierSvc()
	svc.recoveryClaimCompleteFn = func(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
		return &service.BarrierShareClaimResult{ShareIndex: 2, ShareBytes: shareBytes}, nil
	}
	srv := &Server{barrierSvc: svc}

	sig := base64.StdEncoding.EncodeToString([]byte("valid-sig"))
	body := `{"nonce_hex":"aabb","signature":"` + sig + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	injectFakeTLS(req)
	rec := httptest.NewRecorder()
	srv.handleBarrierRecoveryShareClaimComplete(rec, req, "alice")

	assertStatus(t, rec, http.StatusOK)
}

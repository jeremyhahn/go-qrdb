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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package quic

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/initproof/initprooftest"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// handleTenantBarrierInit
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierInit_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := json.Marshal(quicTenantBarrierInitRequest{TenantID: "acme"})

	_, err := srv.handleTenantBarrierInit(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError, got nil")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
	if sncErr.Service != "tenant" {
		t.Errorf("expected service 'tenant', got %q", sncErr.Service)
	}
}

func TestHandleTenantBarrierInit_InvalidJSON(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	_, err := srv.handleTenantBarrierInit(context.Background(), []byte(`{not-json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleTenantBarrierInit_MissingTenantID(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierInitRequest{})

	_, err := srv.handleTenantBarrierInit(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error for missing tenant_id")
	}
	var ie *InvalidArgumentError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InvalidArgumentError, got %T", err)
	}
}

func TestHandleTenantBarrierInit_Success(t *testing.T) {
	called := false
	var gotID, gotPassphrase string
	var gotThreshold, gotShares int
	svc := &mockTenantService{
		barrierInitFn: func(_ context.Context, id string, passphrase []byte, threshold, shares int) error {
			called = true
			gotID = id
			gotPassphrase = string(passphrase)
			gotThreshold = threshold
			gotShares = shares
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierInitRequest{TenantID: "acme", Passphrase: []byte("supplied-secret"), Threshold: 3, Shares: 5})

	resp, err := srv.handleTenantBarrierInit(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var initResp quicTenantBarrierInitResponse
	if jsonErr := json.Unmarshal(resp, &initResp); jsonErr != nil {
		t.Fatalf("unmarshal init response: %v", jsonErr)
	}
	if initResp.TenantID != "acme" {
		t.Errorf("expected tenant_id=acme in init response, got %q", initResp.TenantID)
	}
	if !called {
		t.Fatal("expected TenantBarrierInit to be called")
	}
	if gotID != "acme" {
		t.Errorf("expected id 'acme', got %q", gotID)
	}
	if gotThreshold != 3 {
		t.Errorf("expected threshold 3, got %d", gotThreshold)
	}
	if gotShares != 5 {
		t.Errorf("expected shares 5, got %d", gotShares)
	}
	if gotPassphrase != "supplied-secret" {
		t.Errorf("expected passphrase 'supplied-secret', got %q", gotPassphrase)
	}
}

func TestHandleTenantBarrierInit_ServiceError(t *testing.T) {
	svc := &mockTenantService{
		barrierInitFn: func(_ context.Context, _ string, _ []byte, _, _ int) error {
			return tenant.ErrTenantNotFound
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierInitRequest{TenantID: "missing"})

	_, err := srv.handleTenantBarrierInit(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error from service")
	}
	if !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Fatalf("expected ErrTenantNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantBarrierInitBegin and the proof of possession
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierInitBegin_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := json.Marshal(quicTenantBarrierInitBeginRequest{TenantID: "acme"})

	_, err := srv.handleTenantBarrierInitBegin(context.Background(), payload)
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T (%v)", err, err)
	}
}

func TestHandleTenantBarrierInitBegin_InvalidJSON(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(&mockTenantService{}))

	_, err := srv.handleTenantBarrierInitBegin(context.Background(), []byte(`{not-json`))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T (%v)", err, err)
	}
}

func TestHandleTenantBarrierInitBegin_MissingTenantID(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(&mockTenantService{}))
	payload, _ := json.Marshal(quicTenantBarrierInitBeginRequest{})

	_, err := srv.handleTenantBarrierInitBegin(context.Background(), payload)
	var ie *InvalidArgumentError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InvalidArgumentError, got %T (%v)", err, err)
	}
}

// TestHandleTenantBarrierInitBegin_NoProofAsked pins the unauthenticated
// deployment: with no auth middleware there is no identity to prove, so
// begin answers an empty challenge rather than refusing.
func TestHandleTenantBarrierInitBegin_NoProofAsked(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(&mockTenantService{}))
	payload, _ := json.Marshal(quicTenantBarrierInitBeginRequest{TenantID: "acme"})

	raw, err := srv.handleTenantBarrierInitBegin(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var resp quicTenantBarrierInitBeginResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Nonce) != 0 || resp.Expires != 0 {
		t.Fatalf("challenge = %+v; want none without an auth middleware", resp)
	}
}

// uninitializedQUICTenant reports an uninitialized barrier, the state in
// which init is reachable, and counts the initializations that get through.
func uninitializedQUICTenant(inits *int) *mockTenantService {
	return &mockTenantService{
		barrierStatusFn: func(_ context.Context, id string) (*tenant.TenantBarrierStatus, error) {
			return &tenant.TenantBarrierStatus{TenantID: id}, nil
		},
		barrierInitFn: func(context.Context, string, []byte, int, int) error {
			*inits++
			return nil
		},
	}
}

// quicCtxOnConn binds the identity and the server half of conn the way
// withTLSState and the authenticator bind them on a live QUIC stream.
func quicCtxOnConn(conn *initprooftest.Conn) context.Context {
	return auth.WithIdentity(auth.WithTLSState(context.Background(), &conn.Server),
		&auth.Identity{Subject: "acme-admin", TenantID: "acme"})
}

// TestHandleTenantBarrierInit_ProofOverTheConnection runs the real
// middleware end to end: begin issues a nonce bound to this connection,
// complete redeems it with the client key's signature, and the same proof
// replayed is refused as unauthenticated without reaching the service again.
func TestHandleTenantBarrierInit_ProofOverTheConnection(t *testing.T) {
	inits := 0
	srv := newTestServer(&mockKVService{}, &mockAdminService{},
		WithTenantService(uninitializedQUICTenant(&inits)),
		WithAuthMiddleware(authmw.New(&auth.NoopAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))))
	cert := initprooftest.Certificate(t, "acme-admin")
	conn := initprooftest.Dial(t, cert)
	ctx := quicCtxOnConn(conn)

	payload, _ := json.Marshal(quicTenantBarrierInitBeginRequest{TenantID: "acme"})
	raw, err := srv.handleTenantBarrierInitBegin(ctx, payload)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	var challenge quicTenantBarrierInitBeginResponse
	if err := json.Unmarshal(raw, &challenge); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(challenge.Nonce) == 0 || challenge.Expires == 0 {
		t.Fatalf("challenge = %+v; want a nonce and its expiry", challenge)
	}

	proof := conn.Prove(t, cert.PrivateKey.(crypto.Signer), "acme", challenge.Nonce)
	payload, _ = json.Marshal(quicTenantBarrierInitRequest{
		TenantID:   "acme",
		Passphrase: []byte("pw"),
		Nonce:      proof.Nonce,
		PublicKey:  proof.PublicKey,
		Signature:  proof.Signature,
	})
	if _, err := srv.handleTenantBarrierInit(ctx, payload); err != nil {
		t.Fatalf("complete: %v", err)
	}

	_, err = srv.handleTenantBarrierInit(ctx, payload)
	var proofErr *tenant.FirstAdminProofError
	if !errors.As(err, &proofErr) {
		t.Fatalf("replay err = %T (%v); want *tenant.FirstAdminProofError", err, err)
	}
	if inits != 1 {
		t.Fatalf("tenant service reached %d times; want once", inits)
	}
}

// TestHandleTenantBarrierInit_WithoutProofRefused: with an auth middleware
// configured, complete without a proof is refused before the service runs.
func TestHandleTenantBarrierInit_WithoutProofRefused(t *testing.T) {
	inits := 0
	srv := newTestServer(&mockKVService{}, &mockAdminService{},
		WithTenantService(uninitializedQUICTenant(&inits)),
		WithAuthMiddleware(authmw.New(&auth.NoopAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))))
	conn := initprooftest.Dial(t, initprooftest.Certificate(t, "acme-admin"))

	payload, _ := json.Marshal(quicTenantBarrierInitRequest{TenantID: "acme", Passphrase: []byte("pw")})
	_, err := srv.handleTenantBarrierInit(quicCtxOnConn(conn), payload)
	var proofErr *tenant.FirstAdminProofError
	if !errors.As(err, &proofErr) {
		t.Fatalf("err = %T (%v); want *tenant.FirstAdminProofError", err, err)
	}
	if inits != 0 {
		t.Fatalf("tenant service reached %d times; want never", inits)
	}
}

// ---------------------------------------------------------------------------
// handleTenantBarrierUnseal
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierUnseal_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := json.Marshal(quicTenantBarrierUnsealRequest{TenantID: "acme"})

	_, err := srv.handleTenantBarrierUnseal(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleTenantBarrierUnseal_InvalidJSON(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	_, err := srv.handleTenantBarrierUnseal(context.Background(), []byte(`oops`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleTenantBarrierUnseal_MissingTenantID(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierUnsealRequest{Key: []byte("k")})

	_, err := srv.handleTenantBarrierUnseal(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var ie *InvalidArgumentError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InvalidArgumentError, got %T", err)
	}
}

func TestHandleTenantBarrierUnseal_PassesKey(t *testing.T) {
	var gotMaterial []byte
	svc := &mockTenantService{
		barrierUnsealFn: func(_ context.Context, _ string, key []byte) error {
			gotMaterial = key
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierUnsealRequest{TenantID: "acme", Key: []byte("root-key")})

	if _, err := srv.handleTenantBarrierUnseal(context.Background(), payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(gotMaterial, []byte("root-key")) {
		t.Errorf("expected the key, got %q", gotMaterial)
	}
}

// TestHandleTenantBarrierUnseal_IgnoresShareField proves the credential
// unseal takes no share: a "share" in the frame is not unseal material, so a
// raw share can never stand in for the holder quorum.
func TestHandleTenantBarrierUnseal_IgnoresShareField(t *testing.T) {
	var gotMaterial []byte
	svc := &mockTenantService{
		barrierUnsealFn: func(_ context.Context, _ string, key []byte) error {
			gotMaterial = key
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload := []byte(`{"tenant_id":"acme","share":"c2hhcmUtZGF0YQ=="}`)

	if _, err := srv.handleTenantBarrierUnseal(context.Background(), payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gotMaterial) != 0 {
		t.Errorf("a share reached the credential unseal as %q", gotMaterial)
	}
}

func TestHandleTenantBarrierUnseal_ServiceError(t *testing.T) {
	svc := &mockTenantService{
		barrierUnsealFn: func(_ context.Context, _ string, _ []byte) error {
			return &testError{msg: "unseal failed"}
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierUnsealRequest{TenantID: "acme"})

	_, err := srv.handleTenantBarrierUnseal(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantBarrierSeal
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierSeal_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := json.Marshal(quicTenantBarrierSealRequest{TenantID: "acme"})

	_, err := srv.handleTenantBarrierSeal(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleTenantBarrierSeal_InvalidJSON(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	_, err := srv.handleTenantBarrierSeal(context.Background(), []byte(`)(*&`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleTenantBarrierSeal_MissingTenantID(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierSealRequest{})

	_, err := srv.handleTenantBarrierSeal(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var ie *InvalidArgumentError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InvalidArgumentError, got %T", err)
	}
}

func TestHandleTenantBarrierSeal_Success(t *testing.T) {
	called := false
	var gotID string
	svc := &mockTenantService{
		barrierSealFn: func(_ context.Context, id string) error {
			called = true
			gotID = id
			return nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierSealRequest{TenantID: "acme"})

	resp, err := srv.handleTenantBarrierSeal(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Errorf("expected nil response, got %d bytes", len(resp))
	}
	if !called || gotID != "acme" {
		t.Errorf("expected Seal('acme'), called=%v id=%q", called, gotID)
	}
}

func TestHandleTenantBarrierSeal_ServiceError(t *testing.T) {
	svc := &mockTenantService{
		barrierSealFn: func(_ context.Context, _ string) error {
			return tenant.ErrTenantNotFound
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierSealRequest{TenantID: "missing"})

	_, err := srv.handleTenantBarrierSeal(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Fatalf("expected ErrTenantNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantBarrierStatus
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierStatus_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := json.Marshal(quicTenantBarrierStatusRequest{TenantID: "acme"})

	_, err := srv.handleTenantBarrierStatus(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleTenantBarrierStatus_InvalidJSON(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))

	_, err := srv.handleTenantBarrierStatus(context.Background(), []byte(`bad`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleTenantBarrierStatus_MissingTenantID(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierStatusRequest{})

	_, err := srv.handleTenantBarrierStatus(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var ie *InvalidArgumentError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InvalidArgumentError, got %T", err)
	}
}

func TestHandleTenantBarrierStatus_Success(t *testing.T) {
	svc := &mockTenantService{
		barrierStatusFn: func(_ context.Context, id string) (*tenant.TenantBarrierStatus, error) {
			return &tenant.TenantBarrierStatus{
				TenantID:       id,
				Sealed:         false,
				Initialized:    true,
				Strategy:       "shamir",
				HardwareBacked: false,
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierStatusRequest{TenantID: "acme"})

	resp, err := srv.handleTenantBarrierStatus(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got quicTenantBarrierStatusResponse
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.TenantID != "acme" {
		t.Errorf("expected tenant_id 'acme', got %q", got.TenantID)
	}
	if got.Sealed {
		t.Error("expected Sealed=false")
	}
	if !got.Initialized {
		t.Error("expected Initialized=true")
	}
	if got.Strategy != "shamir" {
		t.Errorf("expected strategy 'shamir', got %q", got.Strategy)
	}
	if got.HardwareBacked {
		t.Error("expected HardwareBacked=false")
	}
}

func TestHandleTenantBarrierStatus_HardwareBacked(t *testing.T) {
	svc := &mockTenantService{
		barrierStatusFn: func(_ context.Context, id string) (*tenant.TenantBarrierStatus, error) {
			return &tenant.TenantBarrierStatus{
				TenantID:       id,
				Sealed:         true,
				Initialized:    true,
				Strategy:       "pkcs11",
				HardwareBacked: true,
			}, nil
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierStatusRequest{TenantID: "acme"})

	resp, err := srv.handleTenantBarrierStatus(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got quicTenantBarrierStatusResponse
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.HardwareBacked {
		t.Error("expected HardwareBacked=true")
	}
	if got.Strategy != "pkcs11" {
		t.Errorf("expected strategy 'pkcs11', got %q", got.Strategy)
	}
}

func TestHandleTenantBarrierStatus_ServiceError(t *testing.T) {
	svc := &mockTenantService{
		barrierStatusFn: func(_ context.Context, _ string) (*tenant.TenantBarrierStatus, error) {
			return nil, tenant.ErrTenantNotFound
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	payload, _ := json.Marshal(quicTenantBarrierStatusRequest{TenantID: "missing"})

	_, err := srv.handleTenantBarrierStatus(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Fatalf("expected ErrTenantNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// missingTenantIDError
// ---------------------------------------------------------------------------

func TestMissingTenantIDError_Message(t *testing.T) {
	e := &missingTenantIDError{}
	if e.Error() == "" {
		t.Error("expected non-empty error message")
	}
}

// ---------------------------------------------------------------------------
// Dispatch registration: confirm the four barrier handlers wire up when a
// tenant service is configured.
// ---------------------------------------------------------------------------

func TestTenantBarrier_DispatchRegistered(t *testing.T) {
	svc := &mockTenantService{}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(svc))
	srv.buildHandlers()
	for _, method := range []string{
		"tenant.BarrierInitBegin",
		"tenant.BarrierInitComplete",
		"tenant.BarrierUnseal",
		"tenant.BarrierSeal",
		"tenant.BarrierStatus",
	} {
		if _, ok := srv.handlers[method]; !ok {
			t.Errorf("expected handler %q to be registered", method)
		}
	}
}

func TestTenantBarrier_DispatchSkippedWhenServiceAbsent(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	srv.buildHandlers()
	for _, method := range []string{
		"tenant.BarrierInitBegin",
		"tenant.BarrierInitComplete",
		"tenant.BarrierUnseal",
		"tenant.BarrierSeal",
		"tenant.BarrierStatus",
	} {
		if _, ok := srv.handlers[method]; ok {
			t.Errorf("expected handler %q to be absent when tenantSvc is nil", method)
		}
	}
}

// ---------------------------------------------------------------------------
// ctxWithFakeTLSQuic — injects a self-signed cert as the mTLS peer cert in ctx
// ---------------------------------------------------------------------------

func ctxWithFakeTLSQuic() context.Context {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic("ctxWithFakeTLSQuic: generate key: " + err.Error())
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-officer"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic("ctxWithFakeTLSQuic: create cert: " + err.Error())
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		panic("ctxWithFakeTLSQuic: parse cert: " + err.Error())
	}
	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	return auth.WithTLSState(context.Background(), state)
}

// ---------------------------------------------------------------------------
// extMockQUICTenantService — wraps mockTenantService with configurable SO fns
// ---------------------------------------------------------------------------

type extMockQUICTenantService struct {
	tenant.TenantServicer
	*mockTenantService
	officerRegisterFn    func(ctx context.Context, tenantID string, req *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error)
	officerListFn        func(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error)
	officerClaimStatusFn func(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error)
	claimBeginFn         func(ctx context.Context, tenantID, username, thumbprint string) (*ceremony.ChallengeResponse, error)
	claimCompleteFn      func(ctx context.Context, tenantID, username, nonceHex string, sig []byte, thumbprint string) (*ceremony.ShareClaimResult, error)
}

func newExtQUICTenantSvc() *extMockQUICTenantService {
	return &extMockQUICTenantService{mockTenantService: &mockTenantService{}}
}

func (e *extMockQUICTenantService) TenantBarrierOfficerRegister(ctx context.Context, tenantID string, req *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	if e.officerRegisterFn != nil {
		return e.officerRegisterFn(ctx, tenantID, req)
	}
	return e.mockTenantService.TenantBarrierOfficerRegister(ctx, tenantID, req)
}
func (e *extMockQUICTenantService) TenantBarrierOfficerList(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error) {
	if e.officerListFn != nil {
		return e.officerListFn(ctx, tenantID)
	}
	return e.mockTenantService.TenantBarrierOfficerList(ctx, tenantID)
}
func (e *extMockQUICTenantService) TenantBarrierShareClaimBegin(ctx context.Context, tenantID, username, thumbprint string) (*ceremony.ChallengeResponse, error) {
	if e.claimBeginFn != nil {
		return e.claimBeginFn(ctx, tenantID, username, thumbprint)
	}
	return e.mockTenantService.TenantBarrierShareClaimBegin(ctx, tenantID, username, thumbprint)
}
func (e *extMockQUICTenantService) TenantBarrierShareClaimComplete(ctx context.Context, tenantID, username, nonceHex string, sig []byte, thumbprint string) (*ceremony.ShareClaimResult, error) {
	if e.claimCompleteFn != nil {
		return e.claimCompleteFn(ctx, tenantID, username, nonceHex, sig, thumbprint)
	}
	return e.mockTenantService.TenantBarrierShareClaimComplete(ctx, tenantID, username, nonceHex, sig, thumbprint)
}

// Delegation pass-through for all other TenantServicer methods.

func (e *extMockQUICTenantService) CreateTenant(ctx context.Context, id, name string, iso tenant.IsolationType, _ ...tenant.CreateTenantOption) (*tenant.TenantInfo, error) {
	return e.mockTenantService.CreateTenant(ctx, id, name, iso)
}
func (e *extMockQUICTenantService) GetTenant(ctx context.Context, id string) (*tenant.TenantInfo, error) {
	return e.mockTenantService.GetTenant(ctx, id)
}
func (e *extMockQUICTenantService) ListTenants(ctx context.Context) ([]tenant.TenantInfo, error) {
	return e.mockTenantService.ListTenants(ctx)
}
func (e *extMockQUICTenantService) UpdateTenant(ctx context.Context, id string, p tenant.TenantPatch) (*tenant.TenantInfo, error) {
	return e.mockTenantService.UpdateTenant(ctx, id, p)
}

// UndoCreateTenant undoes a create the way this double deletes one.
func (e *extMockQUICTenantService) UndoCreateTenant(ctx context.Context, tenantID string) error {
	return e.DeleteTenant(ctx, tenantID)
}

func (e *extMockQUICTenantService) DeleteTenant(ctx context.Context, id string) error {
	return e.mockTenantService.DeleteTenant(ctx, id)
}
func (e *extMockQUICTenantService) UpdateQuotas(ctx context.Context, id string, q *quota.Quotas) (*tenant.TenantInfo, error) {
	return e.mockTenantService.UpdateQuotas(ctx, id, q)
}

func (e *extMockQUICTenantService) UpdateEntitlements(ctx context.Context, id string, ent *quota.Entitlements) (*tenant.TenantInfo, error) {
	return &tenant.TenantInfo{ID: id, Entitlements: ent}, nil
}
func (e *extMockQUICTenantService) TenantBarrierInit(ctx context.Context, id string, passphrase []byte, threshold, total int) (*tenant.TenantBarrierInitResult, error) {
	return e.mockTenantService.TenantBarrierInit(ctx, id, passphrase, threshold, total)
}
func (e *extMockQUICTenantService) TenantBarrierInitWithStrategy(ctx context.Context, id string, passphrase []byte, threshold, total int, sid seal.StrategyID, roster *tenant.TenantBarrierShamirRoster, _ ...tenant.TenantBarrierInitOption) (*tenant.TenantBarrierInitResult, error) {
	return e.mockTenantService.TenantBarrierInitWithStrategy(ctx, id, passphrase, threshold, total, sid, roster)
}
func (e *extMockQUICTenantService) TenantBarrierUnseal(ctx context.Context, id string, key []byte) error {
	return e.mockTenantService.TenantBarrierUnseal(ctx, id, key)
}
func (e *extMockQUICTenantService) TenantBarrierSeal(ctx context.Context, id string) error {
	return e.mockTenantService.TenantBarrierSeal(ctx, id)
}
func (e *extMockQUICTenantService) TenantBarrierStatus(ctx context.Context, id string) (*tenant.TenantBarrierStatus, error) {
	return e.mockTenantService.TenantBarrierStatus(ctx, id)
}
func (e *extMockQUICTenantService) IncrementQuotaUsage(ctx context.Context, id string, r quota.Resource, delta int64) (*tenant.TenantInfo, error) {
	return e.mockTenantService.IncrementQuotaUsage(ctx, id, r, delta)
}
func (e *extMockQUICTenantService) DecrementQuotaUsage(ctx context.Context, id string, r quota.Resource, delta int64) (*tenant.TenantInfo, error) {
	return e.mockTenantService.DecrementQuotaUsage(ctx, id, r, delta)
}
func (e *extMockQUICTenantService) GetQuotaUsage(ctx context.Context, id string, r quota.Resource) (int64, error) {
	return e.mockTenantService.GetQuotaUsage(ctx, id, r)
}

// ---------------------------------------------------------------------------
// handleTenantBarrierUnsealShare — covers the previously-zero function
// ---------------------------------------------------------------------------

// tenantSvcWithUnsealShare embeds extMockQUICTenantService and overrides
// TenantBarrierUnsealShare to return a controllable result.
type tenantSvcWithUnsealShare struct {
	tenant.TenantServicer
	*extMockQUICTenantService
}

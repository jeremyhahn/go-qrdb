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

package mcp

import (
	"context"
	"crypto"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/holderprofile"
	"github.com/jeremyhahn/go-qrdb/pkg/initproof"
	"github.com/jeremyhahn/go-qrdb/pkg/initproof/initprooftest"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// handleTenantBarrierInit
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierInit_EmptyID(t *testing.T) {
	svc := newMockTenantService()
	handler := handleTenantBarrierInit(svc, allowAllTenantInit)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_init_complete", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing tenant_id")
	}
}

func TestHandleTenantBarrierInit_Success(t *testing.T) {
	var gotID, gotPassphrase string
	var gotThreshold, gotShares int
	svc := newMockTenantService()
	svc.barrierInitFn = func(_ context.Context, id string, passphrase []byte, threshold, shares int) error {
		gotID = id
		gotPassphrase = string(passphrase)
		gotThreshold = threshold
		gotShares = shares
		return nil
	}
	handler := handleTenantBarrierInit(svc, allowAllTenantInit)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_init_complete", map[string]any{
		"tenant_id":  "acme",
		"passphrase": "supplied-secret",
		"threshold":  float64(3),
		"shares":     float64(5),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", resultText(t, result))
	}
	if gotID != "acme" || gotPassphrase != "supplied-secret" || gotThreshold != 3 || gotShares != 5 {
		t.Errorf("unexpected args: id=%q passphrase=%q threshold=%d shares=%d", gotID, gotPassphrase, gotThreshold, gotShares)
	}

	text := resultText(t, result)
	var payload struct {
		TenantID string `json:"tenant_id"`
		Strategy string `json:"strategy"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &payload); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if payload.TenantID != "acme" {
		t.Errorf("expected tenant_id=acme in init result, got %q", payload.TenantID)
	}
}

func TestHandleTenantBarrierInit_ServiceError(t *testing.T) {
	svc := newMockTenantService()
	svc.barrierInitFn = func(_ context.Context, _ string, _ []byte, _, _ int) error {
		return tenant.ErrTenantNotFound
	}
	handler := handleTenantBarrierInit(svc, allowAllTenantInit)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_init_complete", map[string]any{
		"tenant_id":  "missing",
		"passphrase": "supplied-secret",
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
	if te.Tool != "qrdb_tenant_barrier_init_complete" {
		t.Errorf("expected tool 'qrdb_tenant_barrier_init_complete', got %q", te.Tool)
	}
}

// denyEverything is the canonical RBAC refusal, over the real
// authmw.AuthMiddleware -- not a stub authorize closure -- so this test
// exercises the production composition (handleTenantBarrierInit wired to
// authmw.AuthorizeTenantBarrierInit) rather than a test double that could
// drift from it.
type denyEverything struct{}

func (denyEverything) Authorize(_ context.Context, _ *auth.Identity, _, _ string) error {
	return auth.ErrUnauthorized
}

// TestHandleTenantBarrierInit_HolderIdentityRefusedEvenWhenUninitialized is
// the MCP half of go-qrdb#560 review item 4: a custodian/SO identity for
// tenant acme, RBAC denying, the barrier never initialized -- exactly the
// state that admits an ORDINARY tenant identity through trust-on-first-init
// -- must still be refused the qrdb_tenant_barrier_init_complete tool.
func TestHandleTenantBarrierInit_HolderIdentityRefusedEvenWhenUninitialized(t *testing.T) {
	svc := newMockTenantService()
	svc.barrierStatusFn = func(_ context.Context, id string) (*tenant.TenantBarrierStatus, error) {
		return &tenant.TenantBarrierStatus{TenantID: id, Initialized: false}, nil
	}
	svc.barrierInitFn = func(_ context.Context, _ string, _ []byte, _, _ int) error {
		t.Fatal("the tenant service must never be reached: a share-holder identity must be refused before the business logic runs")
		return nil
	}

	mw := authmw.New(denyEverything{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))
	authorize := func(ctx context.Context, tenantID string, proof *authmw.TenantBarrierInitProof) error {
		return mw.AuthorizeTenantBarrierInitProof(ctx, tenantID, svc, proof)
	}
	handler := handleTenantBarrierInit(svc, authorize)

	for _, profile := range []string{holderprofile.Custodian, holderprofile.SecurityOfficer} {
		t.Run(profile, func(t *testing.T) {
			id := &auth.Identity{
				Subject:  "custodian",
				TenantID: "acme",
				Metadata: map[string]string{auth.HolderProfileMetadataKey: profile},
			}
			ctx := auth.WithIdentity(context.Background(), id)

			req := buildCallToolRequest(t, "qrdb_tenant_barrier_init_complete", map[string]any{
				"tenant_id":  "acme",
				"passphrase": "x",
			})
			result, err := handler(ctx, req)
			if err != nil {
				t.Fatalf("unexpected transport error: %v", err)
			}
			if !result.IsError {
				t.Fatal("expected an MCP error result: a share-holder certificate must never initialize a tenant barrier")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// handleTenantBarrierInitBegin and the init proof
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierInitBegin_EmptyID(t *testing.T) {
	handler := handleTenantBarrierInitBegin(allowAllTenantInitBegin)
	result, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_tenant_barrier_init_begin", map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing tenant_id")
	}
}

// TestHandleTenantBarrierInitBegin_NoProofAsked proves an unauthenticated
// node's begin answers an empty nonce and no expiry.
func TestHandleTenantBarrierInitBegin_NoProofAsked(t *testing.T) {
	handler := handleTenantBarrierInitBegin(allowAllTenantInitBegin)
	result, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_tenant_barrier_init_begin", map[string]any{"tenant_id": "acme"}))
	if err != nil || result.IsError {
		t.Fatalf("begin: err=%v result=%+v", err, result)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(resultText(t, result)), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["nonce"] != "" {
		t.Errorf("nonce = %v; want empty", payload["nonce"])
	}
	if _, ok := payload["expires"]; ok {
		t.Errorf("expires = %v; want none", payload["expires"])
	}
}

// TestHandleTenantBarrierInitBegin_RefusalIsCoded proves begin's refusal
// reaches the client classified: a connection without an exporter is
// UNAUTHENTICATED.
func TestHandleTenantBarrierInitBegin_RefusalIsCoded(t *testing.T) {
	refuse := func(_ context.Context, tenantID string) (*initproof.Challenge, error) {
		return nil, &tenant.FirstAdminProofError{TenantID: tenantID, Reason: initproof.ReasonNoExporter}
	}
	result, err := handleTenantBarrierInitBegin(refuse)(context.Background(),
		buildCallToolRequest(t, "qrdb_tenant_barrier_init_begin", map[string]any{"tenant_id": "acme"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code := toolErrorCode(t, result); code != wire.CodeUnauthenticated {
		t.Fatalf("code = %v; want UNAUTHENTICATED", code)
	}
}

// TestHandleTenantBarrierInit_ProofArgumentsReachTheGate proves complete
// decodes the base64 proof and hands it to the gate unchanged.
func TestHandleTenantBarrierInit_ProofArgumentsReachTheGate(t *testing.T) {
	var got *authmw.TenantBarrierInitProof
	capture := func(_ context.Context, _ string, proof *authmw.TenantBarrierInitProof) error {
		got = proof
		return nil
	}
	handler := handleTenantBarrierInit(newMockTenantService(), capture)
	result, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_tenant_barrier_init_complete", map[string]any{
		"tenant_id":  "acme",
		"nonce":      base64.StdEncoding.EncodeToString([]byte("n")),
		"public_key": base64.StdEncoding.EncodeToString([]byte("k")),
		"signature":  base64.StdEncoding.EncodeToString([]byte("s")),
	}))
	if err != nil || result.IsError {
		t.Fatalf("complete: err=%v result=%+v", err, result)
	}
	if got == nil || string(got.Nonce) != "n" || string(got.PublicKey) != "k" || string(got.Signature) != "s" {
		t.Fatalf("proof at the gate = %+v", got)
	}
}

// TestHandleTenantBarrierInit_MalformedProofArgumentRefused proves a proof
// argument that is not base64 is refused before the gate runs.
func TestHandleTenantBarrierInit_MalformedProofArgumentRefused(t *testing.T) {
	gate := func(context.Context, string, *authmw.TenantBarrierInitProof) error {
		t.Fatal("the gate must not run on a malformed proof argument")
		return nil
	}
	result, err := handleTenantBarrierInit(newMockTenantService(), gate)(context.Background(),
		buildCallToolRequest(t, "qrdb_tenant_barrier_init_complete", map[string]any{"tenant_id": "acme", "signature": "not base64!"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError || !strings.Contains(resultText(t, result), "signature") {
		t.Fatalf("result = %+v; want a refusal naming the signature argument", result)
	}
}

// allowEverything is the RBAC grant: every permission, every tenant.
type allowEverything struct{}

func (allowEverything) Authorize(context.Context, *auth.Identity, string, string) error { return nil }

// TestHandleTenantBarrierInit_ProofOverTheConnection runs begin and complete
// through the production middleware on a real TLS 1.3 connection: the proof
// by the connection certificate's key is accepted once, and a replay of it is
// refused UNAUTHENTICATED without reaching the tenant service.
func TestHandleTenantBarrierInit_ProofOverTheConnection(t *testing.T) {
	svc := newMockTenantService()
	inits := 0
	svc.barrierInitFn = func(context.Context, string, []byte, int, int) error {
		inits++
		return nil
	}
	mw := authmw.New(allowEverything{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))
	begin := handleTenantBarrierInitBegin(func(ctx context.Context, tenantID string) (*initproof.Challenge, error) {
		return mw.BeginTenantBarrierInit(ctx, tenantID, svc)
	})
	complete := handleTenantBarrierInit(svc, func(ctx context.Context, tenantID string, proof *authmw.TenantBarrierInitProof) error {
		return mw.AuthorizeTenantBarrierInitProof(ctx, tenantID, svc, proof)
	})

	cert := initprooftest.Certificate(t, "acme-admin")
	conn := initprooftest.Dial(t, cert)
	ctx := auth.WithIdentity(auth.WithTLSState(context.Background(), &conn.Server),
		&auth.Identity{Subject: "acme-admin", TenantID: "acme", Roles: []string{"admin"}})

	result, err := begin(ctx, buildCallToolRequest(t, "qrdb_tenant_barrier_init_begin", map[string]any{"tenant_id": "acme"}))
	if err != nil || result.IsError {
		t.Fatalf("begin: err=%v result=%+v", err, result)
	}
	var challenge struct {
		Nonce   []byte `json:"nonce"`
		Expires int64  `json:"expires"`
	}
	if err := json.Unmarshal([]byte(resultText(t, result)), &challenge); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(challenge.Nonce) == 0 || challenge.Expires == 0 {
		t.Fatalf("challenge = %+v; want a nonce and its expiry", challenge)
	}
	proof := conn.Prove(t, cert.PrivateKey.(crypto.Signer), "acme", challenge.Nonce)
	req := buildCallToolRequest(t, "qrdb_tenant_barrier_init_complete", map[string]any{
		"tenant_id":  "acme",
		"passphrase": "pw",
		"nonce":      base64.StdEncoding.EncodeToString(proof.Nonce),
		"public_key": base64.StdEncoding.EncodeToString(proof.PublicKey),
		"signature":  base64.StdEncoding.EncodeToString(proof.Signature),
	})
	result, err = complete(ctx, req)
	if err != nil || result.IsError {
		t.Fatalf("complete: err=%v result=%+v", err, result)
	}

	result, err = complete(ctx, req)
	if err != nil {
		t.Fatalf("replay: unexpected error: %v", err)
	}
	if code := toolErrorCode(t, result); code != wire.CodeUnauthenticated {
		t.Fatalf("replay code = %v; want UNAUTHENTICATED", code)
	}
	if inits != 1 {
		t.Fatalf("tenant service reached %d times; want once", inits)
	}
}

// ---------------------------------------------------------------------------
// handleTenantBarrierUnseal
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierUnseal_EmptyID(t *testing.T) {
	svc := newMockTenantService()
	handler := handleTenantBarrierUnseal(svc, allowAllTenantScope)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_unseal", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing tenant_id")
	}
}

func TestHandleTenantBarrierUnseal_SuccessWithKey(t *testing.T) {
	want := []byte("super-secret")
	var gotID string
	var gotKey []byte
	svc := newMockTenantService()
	svc.barrierUnsealFn = func(_ context.Context, id string, key []byte) error {
		gotID = id
		gotKey = key
		return nil
	}
	handler := handleTenantBarrierUnseal(svc, allowAllTenantScope)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_unseal", map[string]any{
		"tenant_id": "acme",
		"key":       base64.StdEncoding.EncodeToString(want),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", resultText(t, result))
	}
	if gotID != "acme" {
		t.Errorf("expected tenant_id 'acme', got %q", gotID)
	}
	if string(gotKey) != string(want) {
		t.Errorf("expected key %q, got %q", want, gotKey)
	}

	text := resultText(t, result)
	var payload map[string]bool
	if jsonErr := json.Unmarshal([]byte(text), &payload); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if !payload["unsealed"] {
		t.Error("expected unsealed=true")
	}
}

func TestHandleTenantBarrierUnseal_NoKeyDefaults(t *testing.T) {
	var gotKey []byte
	svc := newMockTenantService()
	svc.barrierUnsealFn = func(_ context.Context, _ string, key []byte) error {
		gotKey = key
		return nil
	}
	handler := handleTenantBarrierUnseal(svc, allowAllTenantScope)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_unseal", map[string]any{
		"tenant_id": "acme",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
	if gotKey != nil {
		t.Errorf("expected nil key (use default), got %v", gotKey)
	}
}

func TestHandleTenantBarrierUnseal_BadBase64(t *testing.T) {
	svc := newMockTenantService()
	handler := handleTenantBarrierUnseal(svc, allowAllTenantScope)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_unseal", map[string]any{
		"tenant_id": "acme",
		"key":       "!!!not-base64!!!",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for invalid base64")
	}
}

func TestHandleTenantBarrierUnseal_ServiceError(t *testing.T) {
	svc := newMockTenantService()
	svc.barrierUnsealFn = func(_ context.Context, _ string, _ []byte) error {
		return tenant.ErrTenantNotFound
	}
	handler := handleTenantBarrierUnseal(svc, allowAllTenantScope)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_unseal", map[string]any{
		"tenant_id": "missing",
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleTenantBarrierSeal
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierSeal_EmptyID(t *testing.T) {
	svc := newMockTenantService()
	handler := handleTenantBarrierSeal(svc, allowAllTenantScope)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_seal", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing tenant_id")
	}
}

func TestHandleTenantBarrierSeal_Success(t *testing.T) {
	called := false
	svc := newMockTenantService()
	svc.barrierSealFn = func(_ context.Context, id string) error {
		called = true
		if id != "acme" {
			t.Errorf("expected tenant_id 'acme', got %q", id)
		}
		return nil
	}
	handler := handleTenantBarrierSeal(svc, allowAllTenantScope)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_seal", map[string]any{
		"tenant_id": "acme",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", resultText(t, result))
	}
	if !called {
		t.Error("expected TenantBarrierSeal to be called")
	}

	text := resultText(t, result)
	var payload map[string]bool
	if jsonErr := json.Unmarshal([]byte(text), &payload); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if !payload["sealed"] {
		t.Error("expected sealed=true")
	}
}

func TestHandleTenantBarrierSeal_ServiceError(t *testing.T) {
	svc := newMockTenantService()
	svc.barrierSealFn = func(_ context.Context, _ string) error {
		return errors.New("registry: not initialized")
	}
	handler := handleTenantBarrierSeal(svc, allowAllTenantScope)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_seal", map[string]any{
		"tenant_id": "acme",
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
	if te.Tool != "qrdb_tenant_barrier_seal" {
		t.Errorf("expected tool 'qrdb_tenant_barrier_seal', got %q", te.Tool)
	}
}

// ---------------------------------------------------------------------------
// handleTenantBarrierStatus
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierStatus_EmptyID(t *testing.T) {
	svc := newMockTenantService()
	handler := handleTenantBarrierStatus(svc, allowAllTenantScope)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_status", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing tenant_id")
	}
}

func TestHandleTenantBarrierStatus_Success(t *testing.T) {
	svc := newMockTenantService()
	svc.barrierStatusFn = func(_ context.Context, id string) (*tenant.TenantBarrierStatus, error) {
		return &tenant.TenantBarrierStatus{
			TenantID:       id,
			Sealed:         false,
			Initialized:    true,
			Strategy:       "passphrase",
			HardwareBacked: false,
		}, nil
	}
	handler := handleTenantBarrierStatus(svc, allowAllTenantScope)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_status", map[string]any{
		"tenant_id": "acme",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}

	text := resultText(t, result)
	var status tenant.TenantBarrierStatus
	if jsonErr := json.Unmarshal([]byte(text), &status); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if status.TenantID != "acme" {
		t.Errorf("expected tenant_id 'acme', got %q", status.TenantID)
	}
	if status.Sealed {
		t.Error("expected sealed=false")
	}
	if !status.Initialized {
		t.Error("expected initialized=true")
	}
	if status.Strategy != "passphrase" {
		t.Errorf("expected strategy 'software', got %q", status.Strategy)
	}
}

// TestHandleTenantBarrierStatus_NodesUnreadable pins that a status whose
// serving-node source failed is encoded as nodes_unreadable with no nodes.
func TestHandleTenantBarrierStatus_NodesUnreadable(t *testing.T) {
	svc := newMockTenantService()
	svc.barrierStatusFn = func(_ context.Context, id string) (*tenant.TenantBarrierStatus, error) {
		return &tenant.TenantBarrierStatus{TenantID: id, Sealed: true, NodesUnreadable: true}, nil
	}
	handler := handleTenantBarrierStatus(svc, allowAllTenantScope)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_status", map[string]any{"tenant_id": "acme"})
	result, err := handler(context.Background(), req)
	if err != nil || result.IsError {
		t.Fatalf("handler err = %v, error result = %v", err, result != nil && result.IsError)
	}
	text := resultText(t, result)
	var raw map[string]any
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if raw["nodes_unreadable"] != true {
		t.Fatalf("wire = %s; want nodes_unreadable true", text)
	}
	if _, ok := raw["nodes"]; ok {
		t.Fatalf("wire = %s; want no nodes", text)
	}
}

func TestHandleTenantBarrierStatus_ServiceError(t *testing.T) {
	svc := newMockTenantService()
	svc.barrierStatusFn = func(_ context.Context, _ string) (*tenant.TenantBarrierStatus, error) {
		return nil, tenant.ErrTenantNotFound
	}
	handler := handleTenantBarrierStatus(svc, allowAllTenantScope)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_status", map[string]any{
		"tenant_id": "missing",
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// extMockMCPTenantService — wraps mockTenantService with configurable SO fns
// ---------------------------------------------------------------------------

type extMockMCPTenantService struct {
	tenant.TenantServicer
	*mockTenantService
	officerRegisterFn    func(ctx context.Context, tenantID string, req *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error)
	officerListFn        func(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error)
	officerClaimStatusFn func(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error)
	claimBeginFn         func(ctx context.Context, tenantID, username, thumbprint string) (*ceremony.ChallengeResponse, error)
	claimCompleteFn      func(ctx context.Context, tenantID, username, nonceHex string, sig []byte, thumbprint string) (*ceremony.ShareClaimResult, error)
}

func newExtMCPTenantSvc() *extMockMCPTenantService {
	return &extMockMCPTenantService{mockTenantService: newMockTenantService()}
}

func (e *extMockMCPTenantService) TenantBarrierOfficerRegister(ctx context.Context, tenantID string, req *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	if e.officerRegisterFn != nil {
		return e.officerRegisterFn(ctx, tenantID, req)
	}
	return e.mockTenantService.TenantBarrierOfficerRegister(ctx, tenantID, req)
}
func (e *extMockMCPTenantService) TenantBarrierOfficerList(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error) {
	if e.officerListFn != nil {
		return e.officerListFn(ctx, tenantID)
	}
	return e.mockTenantService.TenantBarrierOfficerList(ctx, tenantID)
}
func (e *extMockMCPTenantService) TenantBarrierShareClaimBegin(ctx context.Context, tenantID, username, thumbprint string) (*ceremony.ChallengeResponse, error) {
	if e.claimBeginFn != nil {
		return e.claimBeginFn(ctx, tenantID, username, thumbprint)
	}
	return e.mockTenantService.TenantBarrierShareClaimBegin(ctx, tenantID, username, thumbprint)
}
func (e *extMockMCPTenantService) TenantBarrierShareClaimComplete(ctx context.Context, tenantID, username, nonceHex string, sig []byte, thumbprint string) (*ceremony.ShareClaimResult, error) {
	if e.claimCompleteFn != nil {
		return e.claimCompleteFn(ctx, tenantID, username, nonceHex, sig, thumbprint)
	}
	return e.mockTenantService.TenantBarrierShareClaimComplete(ctx, tenantID, username, nonceHex, sig, thumbprint)
}

// Delegation pass-through for all other TenantServicer methods.

func (e *extMockMCPTenantService) CreateTenant(ctx context.Context, id, name string, iso tenant.IsolationType, _ ...tenant.CreateTenantOption) (*tenant.TenantInfo, error) {
	return e.mockTenantService.CreateTenant(ctx, id, name, iso)
}
func (e *extMockMCPTenantService) GetTenant(ctx context.Context, id string) (*tenant.TenantInfo, error) {
	return e.mockTenantService.GetTenant(ctx, id)
}
func (e *extMockMCPTenantService) ListTenants(ctx context.Context) ([]tenant.TenantInfo, error) {
	return e.mockTenantService.ListTenants(ctx)
}
func (e *extMockMCPTenantService) UpdateTenant(ctx context.Context, id string, p tenant.TenantPatch) (*tenant.TenantInfo, error) {
	return e.mockTenantService.UpdateTenant(ctx, id, p)
}

// UndoCreateTenant undoes a create the way this double deletes one.
func (e *extMockMCPTenantService) UndoCreateTenant(ctx context.Context, tenantID string) error {
	return e.DeleteTenant(ctx, tenantID)
}

func (e *extMockMCPTenantService) DeleteTenant(ctx context.Context, id string) error {
	return e.mockTenantService.DeleteTenant(ctx, id)
}
func (e *extMockMCPTenantService) UpdateQuotas(ctx context.Context, id string, q *quota.Quotas) (*tenant.TenantInfo, error) {
	return e.mockTenantService.UpdateQuotas(ctx, id, q)
}

func (e *extMockMCPTenantService) UpdateEntitlements(ctx context.Context, id string, ent *quota.Entitlements) (*tenant.TenantInfo, error) {
	return &tenant.TenantInfo{ID: id, Entitlements: ent}, nil
}
func (e *extMockMCPTenantService) TenantBarrierInit(ctx context.Context, id string, passphrase []byte, threshold, total int) (*tenant.TenantBarrierInitResult, error) {
	return e.mockTenantService.TenantBarrierInit(ctx, id, passphrase, threshold, total)
}
func (e *extMockMCPTenantService) TenantBarrierInitWithStrategy(ctx context.Context, id string, passphrase []byte, threshold, total int, sid seal.StrategyID, roster *tenant.TenantBarrierShamirRoster, _ ...tenant.TenantBarrierInitOption) (*tenant.TenantBarrierInitResult, error) {
	return e.mockTenantService.TenantBarrierInitWithStrategy(ctx, id, passphrase, threshold, total, sid, roster)
}
func (e *extMockMCPTenantService) TenantBarrierUnseal(ctx context.Context, id string, key []byte) error {
	return e.mockTenantService.TenantBarrierUnseal(ctx, id, key)
}
func (e *extMockMCPTenantService) TenantBarrierSeal(ctx context.Context, id string) error {
	return e.mockTenantService.TenantBarrierSeal(ctx, id)
}
func (e *extMockMCPTenantService) TenantBarrierStatus(ctx context.Context, id string) (*tenant.TenantBarrierStatus, error) {
	return e.mockTenantService.TenantBarrierStatus(ctx, id)
}
func (e *extMockMCPTenantService) IncrementQuotaUsage(ctx context.Context, id string, r quota.Resource, delta int64) (*tenant.TenantInfo, error) {
	return e.mockTenantService.IncrementQuotaUsage(ctx, id, r, delta)
}
func (e *extMockMCPTenantService) DecrementQuotaUsage(ctx context.Context, id string, r quota.Resource, delta int64) (*tenant.TenantInfo, error) {
	return e.mockTenantService.DecrementQuotaUsage(ctx, id, r, delta)
}
func (e *extMockMCPTenantService) GetQuotaUsage(ctx context.Context, id string, r quota.Resource) (int64, error) {
	return e.mockTenantService.GetQuotaUsage(ctx, id, r)
}

// ---------------------------------------------------------------------------
// Tenant barrier SO MCP handler tests
// ---------------------------------------------------------------------------

// handleTenantBarrierShareClaimBegin — mcpCertThumbprint error path.

// handleTenantBarrierShareClaimComplete tests.

// handleTenantBarrierShamirSealVote tests.

// TestTenantBarrierHandlers_ScopeDenied verifies every tenant-barrier tool
// handler refuses to invoke the tenant service when the fine-grained
// target-tenant authorizer denies the call, returning an MCP error result
// rather than a transport error.
func TestTenantBarrierHandlers_ScopeDenied(t *testing.T) {
	denied := errors.New("rbac: scope denied")
	deny := func(_ context.Context, gotTenant string, _ permission.Permission) error {
		if gotTenant != "victim" {
			t.Errorf("authorizer received tenant %q, want %q", gotTenant, "victim")
		}
		return denied
	}

	svc := newMockTenantService()
	svc.barrierInitFn = func(_ context.Context, _ string, _ []byte, _, _ int) error {
		t.Error("service must not be reached on scope denial")
		return nil
	}
	svc.barrierUnsealFn = func(_ context.Context, _ string, _ []byte) error {
		t.Error("service must not be reached on scope denial")
		return nil
	}
	svc.barrierSealFn = func(_ context.Context, _ string) error {
		t.Error("service must not be reached on scope denial")
		return nil
	}
	svc.barrierStatusFn = func(_ context.Context, _ string) (*tenant.TenantBarrierStatus, error) {
		t.Error("service must not be reached on scope denial")
		return nil, nil
	}

	cases := []struct {
		name    string
		handler mcpserver.ToolHandlerFunc
		args    map[string]any
	}{
		{"init_begin", handleTenantBarrierInitBegin(func(ctx context.Context, tenantID string) (*initproof.Challenge, error) {
			return nil, deny(ctx, tenantID, permission.TenantBarrierInit)
		}), map[string]any{"tenant_id": "victim"}},
		{"init_complete", handleTenantBarrierInit(svc, func(ctx context.Context, tenantID string, _ *authmw.TenantBarrierInitProof) error {
			return deny(ctx, tenantID, permission.TenantBarrierInit)
		}), map[string]any{"tenant_id": "victim", "passphrase": "x"}},
		{"unseal", handleTenantBarrierUnseal(svc, deny), map[string]any{"tenant_id": "victim"}},
		{"seal", handleTenantBarrierSeal(svc, deny), map[string]any{"tenant_id": "victim"}},
		{"status", handleTenantBarrierStatus(svc, deny), map[string]any{"tenant_id": "victim"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := buildCallToolRequest(t, "qrdb_tenant_barrier_"+tc.name, tc.args)
			result, err := tc.handler(context.Background(), req)
			if err != nil {
				t.Fatalf("expected MCP error result, got transport error: %v", err)
			}
			if !result.IsError {
				t.Fatal("expected IsError=true on scope denial")
			}
			if text := resultText(t, result); !strings.Contains(text, "authorization denied") {
				t.Errorf("expected 'authorization denied' in result, got %q", text)
			}
		})
	}
}

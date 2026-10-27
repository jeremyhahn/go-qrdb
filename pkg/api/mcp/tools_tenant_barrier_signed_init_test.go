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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// signedInitRecorder records the requests both halves of a signed init reach
// the tenant service with.
type signedInitRecorder struct {
	*mockTenantService
	begin       *tenant.TenantBarrierInitBeginRequest
	complete    *tenant.TenantBarrierInitCompleteRequest
	passphrase  []byte
	completeErr error
}

func newSignedInitRecorder() *signedInitRecorder {
	return &signedInitRecorder{mockTenantService: newMockTenantService()}
}

func (r *signedInitRecorder) TenantBarrierInitBegin(_ context.Context, req *tenant.TenantBarrierInitBeginRequest) (*tenant.TenantBarrierInitBeginResult, error) {
	r.begin = req
	return &tenant.TenantBarrierInitBeginResult{
		TenantID: req.TenantID, Strategy: string(req.Strategy), Reference: []byte("ref"), Statement: []byte("stmt"),
		ExpiresAt: time.Unix(1_800_000_000, 0).UTC(), Threshold: req.Threshold, TotalShares: req.Shares,
	}, nil
}

func (r *signedInitRecorder) TenantBarrierInitComplete(_ context.Context, req *tenant.TenantBarrierInitCompleteRequest) (*tenant.TenantBarrierInitResult, error) {
	r.complete = req
	r.passphrase = req.Passphrase
	if r.completeErr != nil {
		return nil, r.completeErr
	}
	return &tenant.TenantBarrierInitResult{TenantID: req.TenantID, Strategy: "passphrase"}, nil
}

func allowSignedInit(context.Context, string) error { return nil }

func TestHandleTenantBarrierSignedInitBegin_MapsArgumentsAndResult(t *testing.T) {
	svc := newSignedInitRecorder()
	result, err := handleTenantBarrierSignedInitBegin(svc, allowSignedInit)(context.Background(), buildCallToolRequest(t, toolNameTenantBarrierSignedInitBegin, map[string]any{
		"tenant_id": "acme", "public_key": "c3BraQ==", "strategy": "shamir", "key_ref": "kr", "threshold": 2, "shares": 3,
		"officers": `[{"username":"alice","role":"so"}]`, "policy": "any-enrolled",
	}))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	got := svc.begin
	if got.TenantID != "acme" || got.Strategy != seal.StrategyID("shamir") || got.KeyRef != "kr" || got.Threshold != 2 || got.Shares != 3 ||
		string(got.SignerPublicKey) != "spki" || got.Roster == nil || got.Roster.Policy != seal.FactorPolicy("any-enrolled") {
		t.Fatalf("service request = %+v", got)
	}
	var resp transport.TenantBarrierSignedInitBeginResponse
	if err := json.Unmarshal([]byte(resultText(t, result)), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(resp.Reference) != "ref" || string(resp.Statement) != "stmt" || !resp.ExpiresAt.Equal(time.Unix(1_800_000_000, 0)) {
		t.Fatalf("response = %+v", resp)
	}
}

func TestHandleTenantBarrierSignedInitBegin_RefusedArguments(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"no tenant":      {"public_key": "c3BraQ=="},
		"bad public key": {"tenant_id": "acme", "public_key": "not base64!"},
		"bad officers":   {"tenant_id": "acme", "public_key": "c3BraQ==", "officers": "{"},
	} {
		t.Run(name, func(t *testing.T) {
			svc := newSignedInitRecorder()
			result, err := handleTenantBarrierSignedInitBegin(svc, allowSignedInit)(context.Background(), buildCallToolRequest(t, toolNameTenantBarrierSignedInitBegin, args))
			if err != nil || result == nil || !result.IsError {
				t.Fatalf("result %+v, err %v: want a tool error", result, err)
			}
			if svc.begin != nil {
				t.Fatal("service reached by a refused request")
			}
		})
	}
}

func TestHandleTenantBarrierSignedInitBegin_Unauthorized(t *testing.T) {
	svc := newSignedInitRecorder()
	deny := func(context.Context, string) error { return errors.New("denied") }
	result, err := handleTenantBarrierSignedInitBegin(svc, deny)(context.Background(), buildCallToolRequest(t, toolNameTenantBarrierSignedInitBegin, map[string]any{"tenant_id": "acme"}))
	if err != nil || result == nil || !result.IsError || svc.begin != nil {
		t.Fatalf("result %+v, err %v, service reached %v: want a refusal before the service", result, err, svc.begin != nil)
	}
}

func TestHandleTenantBarrierSignedInitComplete_MapsArgumentsAndWipesPassphrase(t *testing.T) {
	svc := newSignedInitRecorder()
	result, err := handleTenantBarrierSignedInitComplete(svc, allowSignedInit)(context.Background(), buildCallToolRequest(t, toolNameTenantBarrierSignedInitComplete, map[string]any{
		"tenant_id": "acme", "reference": "cmVm", "signature": "c2ln", "passphrase": "secret-passphrase",
	}))
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if string(svc.complete.Reference) != "ref" || string(svc.complete.Signature) != "sig" {
		t.Fatalf("service request = %+v", svc.complete)
	}
	if len(svc.passphrase) == 0 || !bytes.Equal(svc.passphrase, make([]byte, len(svc.passphrase))) {
		t.Fatalf("passphrase %q not wiped", svc.passphrase)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(resultText(t, result)), &resp); err != nil || resp["strategy"] != "passphrase" {
		t.Fatalf("response %v, err %v", resp, err)
	}
}

func TestHandleTenantBarrierSignedInitComplete_Refusals(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"no tenant":     {"reference": "cmVm"},
		"bad reference": {"tenant_id": "acme", "reference": "not base64!"},
		"bad signature": {"tenant_id": "acme", "reference": "cmVm", "signature": "not base64!"},
	} {
		t.Run(name, func(t *testing.T) {
			svc := newSignedInitRecorder()
			result, err := handleTenantBarrierSignedInitComplete(svc, allowSignedInit)(context.Background(), buildCallToolRequest(t, toolNameTenantBarrierSignedInitComplete, args))
			if err != nil || result == nil || !result.IsError || svc.complete != nil {
				t.Fatalf("result %+v, err %v: want a tool error before the service", result, err)
			}
		})
	}
	svc := newSignedInitRecorder()
	svc.completeErr = &tenant.SignedInitError{TenantID: "acme", Reason: tenant.SignedInitUnknown}
	_, err := handleTenantBarrierSignedInitComplete(svc, allowSignedInit)(context.Background(), buildCallToolRequest(t, toolNameTenantBarrierSignedInitComplete, map[string]any{
		"tenant_id": "acme", "reference": "cmVm", "signature": "c2ln",
	}))
	if !errors.As(err, new(*tenant.SignedInitError)) {
		t.Fatalf("replay: err = %v, want *tenant.SignedInitError", err)
	}
}

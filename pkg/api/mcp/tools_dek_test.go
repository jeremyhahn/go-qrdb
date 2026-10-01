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

package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// dekTestStores resolves one in-memory store per tenant.
type dekTestStores map[string]*kvstore.MemoryKVStore

func (d dekTestStores) Resolve(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	s, ok := d[tenantID]
	if !ok {
		s = kvstore.NewMemoryKVStore()
		d[tenantID] = s
	}
	return s, nil
}

// newTestDEKService is the production DEK service over in-memory stores.
func newTestDEKService(t *testing.T) service.DEKServicer {
	t.Helper()
	keyring, err := dek.NewKeyring(dekTestStores{})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := service.NewDEKService(keyring)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func dekCall(t *testing.T, ctx context.Context, h func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error), args map[string]any, out any) error {
	t.Helper()
	res, err := h(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: args}})
	if err != nil {
		return err
	}
	if res.IsError {
		t.Fatalf("tool error result: %+v", res.Content)
	}
	text := res.Content[0].(mcp.TextContent).Text
	if err := json.Unmarshal([]byte(text), out); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	return nil
}

func TestDEKToolsRoundTripRotateRewrapRetire(t *testing.T) {
	svc := newTestDEKService(t)
	ctx := tenant.WithTenantID(context.Background(), "acme")
	b64 := base64.StdEncoding.EncodeToString

	var enc transport.TenantDEKEncryptResponse
	if err := dekCall(t, ctx, handleDEKEncrypt(svc), map[string]any{"plaintext": b64([]byte("s3cret")), "aad": b64([]byte("p"))}, &enc); err != nil {
		t.Fatal(err)
	}
	var st transport.TenantDEKStatusResponse
	if err := dekCall(t, ctx, handleDEKRotate(svc), map[string]any{}, &st); err != nil || st.CurrentVersion != 2 {
		t.Fatalf("rotate: %+v, %v", st, err)
	}
	var rw transport.TenantDEKRewrapResponse
	if err := dekCall(t, ctx, handleDEKRewrap(svc), map[string]any{"ciphertext": b64(enc.Ciphertext), "aad": b64([]byte("p"))}, &rw); err != nil || rw.ToVersion != 2 {
		t.Fatalf("rewrap: %+v, %v", rw, err)
	}
	if err := dekCall(t, ctx, handleDEKRetire(svc), map[string]any{"version": float64(1)}, &st); err != nil || st.Versions[0].RetiredAt == nil {
		t.Fatalf("retire: %+v, %v", st, err)
	}
	var dec transport.TenantDEKDecryptResponse
	if err := dekCall(t, ctx, handleDEKDecrypt(svc), map[string]any{"ciphertext": b64(rw.Ciphertext), "aad": b64([]byte("p"))}, &dec); err != nil || string(dec.Plaintext) != "s3cret" {
		t.Fatalf("decrypt: %+v, %v", dec, err)
	}
	if err := dekCall(t, ctx, handleDEKStatus(svc), map[string]any{}, &st); err != nil || st.TenantID != "acme" {
		t.Fatalf("status: %+v, %v", st, err)
	}
}

// TestDEKToolErrorsCarryTheSharedClassification: a cross-tenant call is
// PermissionDenied and a retire of the current version FailedPrecondition on
// MCP exactly as on gRPC.
func TestDEKToolErrorsCarryTheSharedClassification(t *testing.T) {
	svc := newTestDEKService(t)
	ctx := tenant.WithTenantID(context.Background(), "acme")
	var st transport.TenantDEKStatusResponse
	err := dekCall(t, ctx, handleDEKStatus(svc), map[string]any{"tenant_id": "globex"}, &st)
	if code := classifyToolError(err); code != wire.CodePermissionDenied {
		t.Fatalf("cross-tenant: code %v (err %v), want PermissionDenied", code, err)
	}
	if err := dekCall(t, ctx, handleDEKRotate(svc), map[string]any{}, &st); err != nil {
		t.Fatal(err)
	}
	err = dekCall(t, ctx, handleDEKRetire(svc), map[string]any{"version": float64(1)}, &st)
	if code := classifyToolError(err); code != wire.CodeFailedPrecondition {
		t.Fatalf("retire current: code %v (err %v), want FailedPrecondition", code, err)
	}
}

func TestDEKToolsEncryptAnEmptyValue(t *testing.T) {
	svc := newTestDEKService(t)
	ctx := tenant.WithTenantID(context.Background(), "acme")
	var enc transport.TenantDEKEncryptResponse
	if err := dekCall(t, ctx, handleDEKEncrypt(svc), map[string]any{"plaintext": ""}, &enc); err != nil {
		t.Fatal(err)
	}
	var dec transport.TenantDEKDecryptResponse
	ct := base64.StdEncoding.EncodeToString(enc.Ciphertext)
	if err := dekCall(t, ctx, handleDEKDecrypt(svc), map[string]any{"ciphertext": ct}, &dec); err != nil || len(dec.Plaintext) != 0 {
		t.Fatalf("decrypt empty: %+v, %v", dec, err)
	}
}

func TestDEKToolsRejectBadArguments(t *testing.T) {
	svc := newTestDEKService(t)
	ctx := context.Background()
	for name, c := range map[string]struct {
		h    func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
		args map[string]any
	}{
		"missing plaintext": {handleDEKEncrypt(svc), map[string]any{}},
		"bad plaintext":     {handleDEKEncrypt(svc), map[string]any{"plaintext": "!!"}},
		"bad aad":           {handleDEKDecrypt(svc), map[string]any{"ciphertext": "AA==", "aad": "!!"}},
		"missing cipher":    {handleDEKRewrap(svc), map[string]any{}},
	} {
		res, err := c.h(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: c.args}})
		if err != nil || res == nil || !res.IsError {
			t.Errorf("%s: res=%+v err=%v, want a tool error result", name, res, err)
		}
	}
}

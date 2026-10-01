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
	"bytes"
	"context"
	"errors"
	"testing"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// dekStores resolves one in-memory store per tenant.
type dekStores map[string]*kvstore.MemoryKVStore

func (d dekStores) Resolve(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	s, ok := d[tenantID]
	if !ok {
		s = kvstore.NewMemoryKVStore()
		d[tenantID] = s
	}
	return s, nil
}

// connectDEKInProcess connects the SDK to the REAL MCP server tools over the
// production keyring service.
func connectDEKInProcess(t *testing.T) *Transport {
	t.Helper()
	keyring, err := dek.NewKeyring(dekStores{})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := service.NewDEKService(keyring)
	if err != nil {
		t.Fatal(err)
	}
	srv := mcpapi.NewServer(newMockKVService(), newMockAdminService(), ":0", mcpapi.WithDEKService(svc))
	tr, err := NewInProcess(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

func TestDEK_RoundTripThroughTheServerTools(t *testing.T) {
	tr := connectDEKInProcess(t)
	ctx := context.Background()
	aad := []byte("tenant/path/v1")

	enc, err := tr.TenantDEKEncrypt(ctx, &transport.TenantDEKEncryptRequest{Plaintext: []byte("s3cret"), AAD: aad})
	if err != nil || enc.Version != 1 {
		t.Fatalf("encrypt = %+v, %v", enc, err)
	}
	st, err := tr.TenantDEKRotate(ctx, &transport.TenantDEKRequest{})
	if err != nil || st.CurrentVersion != 2 {
		t.Fatalf("rotate = %+v, %v", st, err)
	}
	rw, err := tr.TenantDEKRewrap(ctx, &transport.TenantDEKRewrapRequest{Ciphertext: enc.Ciphertext, AAD: aad})
	if err != nil || rw.FromVersion != 1 || rw.ToVersion != 2 {
		t.Fatalf("rewrap = %+v, %v", rw, err)
	}
	if st, err = tr.TenantDEKRetire(ctx, &transport.TenantDEKRetireRequest{Version: 1}); err != nil || st.Versions[0].RetiredAt == nil {
		t.Fatalf("retire = %+v, %v", st, err)
	}
	dec, err := tr.TenantDEKDecrypt(ctx, &transport.TenantDEKDecryptRequest{Ciphertext: rw.Ciphertext, AAD: aad})
	if err != nil || !bytes.Equal(dec.Plaintext, []byte("s3cret")) || dec.Version != 2 {
		t.Fatalf("decrypt = %+v, %v", dec, err)
	}
	if st, err = tr.TenantDEKStatus(ctx, &transport.TenantDEKRequest{}); err != nil || len(st.Versions) != 2 {
		t.Fatalf("status = %+v, %v", st, err)
	}
}

// TestDEK_RefusalsReachTheClient: the server's refusals arrive as errors.
func TestDEK_RefusalsReachTheClient(t *testing.T) {
	tr := connectDEKInProcess(t)
	ctx := context.Background()
	enc, err := tr.TenantDEKEncrypt(ctx, &transport.TenantDEKEncryptRequest{Plaintext: []byte("x"), AAD: []byte("a")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.TenantDEKDecrypt(ctx, &transport.TenantDEKDecryptRequest{Ciphertext: enc.Ciphertext, AAD: []byte("b")}); err == nil {
		t.Error("a mismatched AAD must fail")
	}
	if _, err := tr.TenantDEKRetire(ctx, &transport.TenantDEKRetireRequest{Version: 1}); err == nil {
		t.Error("retiring the current version must fail")
	}
	if _, err := tr.TenantDEKStatus(ctx, &transport.TenantDEKRequest{TenantID: "another"}); err == nil {
		t.Error("a cross-tenant request must fail")
	}
}

func TestDEK_NilRequests(t *testing.T) {
	tr := connectDEKInProcess(t)
	ctx := context.Background()
	checks := map[string]error{}
	_, checks["status"] = tr.TenantDEKStatus(ctx, nil)
	_, checks["rotate"] = tr.TenantDEKRotate(ctx, nil)
	_, checks["retire"] = tr.TenantDEKRetire(ctx, nil)
	_, checks["encrypt"] = tr.TenantDEKEncrypt(ctx, nil)
	_, checks["decrypt"] = tr.TenantDEKDecrypt(ctx, nil)
	_, checks["rewrap"] = tr.TenantDEKRewrap(ctx, nil)
	for op, err := range checks {
		var nre *transport.NilRequestError
		if !errors.As(err, &nre) {
			t.Errorf("%s(nil): err = %v, want *transport.NilRequestError", op, err)
		}
	}
}

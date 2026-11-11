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
	"errors"
	"testing"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/keywrap"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// fakeDEKKeyWrapProvider is a same-shaped keywrap.Provider test double --
// it "wraps" by copying bytes verbatim, the identical shape
// pkg/keywrap's own fakeHardwareProvider test double uses.
type fakeDEKKeyWrapProvider struct{}

func (fakeDEKKeyWrapProvider) ProviderID() string { return keywrap.SoftwareProviderID }

func (fakeDEKKeyWrapProvider) Wrap(_ context.Context, _ string, dek []byte) ([]byte, error) {
	out := make([]byte, len(dek))
	copy(out, dek)
	return out, nil
}

func (fakeDEKKeyWrapProvider) Unwrap(_ context.Context, _ string, wrapped []byte) ([]byte, error) {
	out := make([]byte, len(wrapped))
	copy(out, wrapped)
	return out, nil
}

// testDEKKeyWrapRegistry returns a Registry whose "software" entry is
// fakeDEKKeyWrapProvider, so a test gets a fully working DEK keyring with
// no filesystem key directory.
func testDEKKeyWrapRegistry() keywrap.Registry {
	r := keywrap.NewRegistry()
	_ = r.Register(keywrap.SoftwareProviderID, func() (keywrap.Provider, error) {
		return fakeDEKKeyWrapProvider{}, nil
	})
	return r
}

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
	keyring, err := dek.NewKeyring(dekStores{}, dek.WithKeyWrapRegistry(testDEKKeyWrapRegistry()))
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

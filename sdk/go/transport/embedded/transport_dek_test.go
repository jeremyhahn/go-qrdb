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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/keywrap"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
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

// newDEKTransport returns an embedded transport over the production keyring
// service.
func newDEKTransport(t *testing.T) *Transport {
	t.Helper()
	keyring, err := dek.NewKeyring(dekStores{}, dek.WithKeyWrapRegistry(testDEKKeyWrapRegistry()))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := service.NewDEKService(keyring)
	if err != nil {
		t.Fatal(err)
	}
	return newExtTransport(t, WithDEKService(svc))
}

func TestEmbeddedDEK_RefusesAnotherTenant(t *testing.T) {
	tr := newDEKTransport(t)
	ctx := tenant.WithTenantID(context.Background(), "acme")
	_, err := tr.TenantDEKEncrypt(ctx, &transport.TenantDEKEncryptRequest{TenantID: "globex", Plaintext: []byte("x")})
	if !errors.Is(err, dek.ErrCrossTenant) {
		t.Fatalf("err = %v, want dek.ErrCrossTenant", err)
	}
}

func TestEmbeddedDEK_NilServiceNilRequestAndClosed(t *testing.T) {
	ctx := context.Background()
	unwired := newExtTransport(t)
	var nse *NilServiceError
	if _, err := unwired.TenantDEKStatus(ctx, &transport.TenantDEKRequest{}); !errors.As(err, &nse) {
		t.Errorf("unwired: %v", err)
	}

	tr := newDEKTransport(t)
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
			t.Errorf("%s(nil): %v", op, err)
		}
	}

	_ = tr.Close()
	var ce *transport.ClosedError
	if _, err := tr.TenantDEKRewrap(ctx, &transport.TenantDEKRewrapRequest{}); !errors.As(err, &ce) {
		t.Errorf("after close: %v", err)
	}
}

func TestEmbeddedDEK_GenericRequestDispatch(t *testing.T) {
	tr := newDEKTransport(t)
	ctx := context.Background()
	var enc *transport.TenantDEKEncryptResponse
	if err := tr.Request(ctx, "TenantDEKEncrypt", &transport.TenantDEKEncryptRequest{Plaintext: []byte("x")}, &enc); err != nil || enc == nil || enc.Version != 1 {
		t.Fatalf("Request(TenantDEKEncrypt) = %+v, %v", enc, err)
	}
	var st *transport.TenantDEKStatusResponse
	if err := tr.Request(ctx, "TenantDEKStatus", &transport.TenantDEKRequest{}, &st); err != nil || st.CurrentVersion != 1 {
		t.Fatalf("Request(TenantDEKStatus) = %+v, %v", st, err)
	}
	var inv *InvalidRequestError
	if err := tr.Request(ctx, "TenantDEKRewrap", 5, nil); !errors.As(err, &inv) {
		t.Fatalf("bad request type: %v", err)
	}
	if err := tr.Request(ctx, "TenantDEKRetire", &transport.TenantDEKRetireRequest{Version: 1}, nil); !errors.Is(err, dek.ErrRetireCurrentVersion) {
		t.Fatalf("dispatch error passthrough: %v", err)
	}
}

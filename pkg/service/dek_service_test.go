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

package service

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// perTenantStores resolves one in-memory store per tenant.
type perTenantStores map[string]*kvstore.MemoryKVStore

func (p perTenantStores) Resolve(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	s, ok := p[tenantID]
	if !ok {
		s = kvstore.NewMemoryKVStore()
		p[tenantID] = s
	}
	return s, nil
}

func newTestDEKService(t *testing.T) *DEKService {
	t.Helper()
	k, err := dek.NewKeyring(perTenantStores{})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewDEKService(k)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func asTenant(id string) context.Context {
	return tenant.WithTenantID(context.Background(), id)
}

func TestNewDEKServiceRefusesANilKeyring(t *testing.T) {
	if _, err := NewDEKService(nil); !errors.Is(err, dek.ErrInvalidInput) {
		t.Fatalf("err = %v, want dek.ErrInvalidInput", err)
	}
}

func TestDEKServiceActsOnTheCallersTenant(t *testing.T) {
	svc := newTestDEKService(t)
	ctx := asTenant("acme")
	s, err := svc.Encrypt(ctx, "", []byte("v"), []byte("aad"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	// Naming the caller's own tenant explicitly is the same operation.
	o, err := svc.Decrypt(ctx, "acme", s.Ciphertext, []byte("aad"))
	if err != nil || !bytes.Equal(o.Plaintext, []byte("v")) {
		t.Fatalf("Decrypt = %q, %v", o.Plaintext, err)
	}
	st, err := svc.Status(ctx, "")
	if err != nil || st.TenantID != "acme" || st.Current != 1 {
		t.Fatalf("Status = %+v, %v", st, err)
	}
}

// TestDEKServiceRefusesCrossTenantForEveryOperation includes the system
// tenant: the system-tenant rule that lifts self-scope permissions across
// tenants does not reach another tenant's keyring.
func TestDEKServiceRefusesCrossTenantForEveryOperation(t *testing.T) {
	svc := newTestDEKService(t)
	s, err := svc.Encrypt(asTenant("acme"), "", []byte("v"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, caller := range []string{"globex", tenant.SystemTenantID} {
		ctx := asTenant(caller)
		checks := map[string]error{}
		_, checks["encrypt"] = svc.Encrypt(ctx, "acme", []byte("v"), nil)
		_, checks["decrypt"] = svc.Decrypt(ctx, "acme", s.Ciphertext, nil)
		_, checks["rewrap"] = svc.Rewrap(ctx, "acme", s.Ciphertext, nil)
		_, checks["rotate"] = svc.Rotate(ctx, "acme")
		_, checks["retire"] = svc.Retire(ctx, "acme", 1)
		_, checks["status"] = svc.Status(ctx, "acme")
		for op, err := range checks {
			var ct *dek.CrossTenantError
			if !errors.As(err, &ct) || ct.CallerTenant != caller || ct.RequestedTenant != "acme" {
				t.Errorf("%s as %s: err = %v, want *dek.CrossTenantError", op, caller, err)
			}
		}
	}
}

// TestDEKServiceUnboundCallerIsTheSystemTenant: a context with no tenant
// binding executes against the system tenant, as everywhere else in qrdb.
func TestDEKServiceUnboundCallerIsTheSystemTenant(t *testing.T) {
	svc := newTestDEKService(t)
	st, err := svc.Rotate(context.Background(), "")
	if err != nil || st.TenantID != tenant.SystemTenantID {
		t.Fatalf("Rotate = %+v, %v", st, err)
	}
	if _, err := svc.Rotate(context.Background(), "acme"); !errors.Is(err, dek.ErrCrossTenant) {
		t.Fatalf("unbound caller naming acme: err = %v, want ErrCrossTenant", err)
	}
}

func TestDEKServiceLifecycle(t *testing.T) {
	svc := newTestDEKService(t)
	ctx := asTenant("acme")
	old, err := svc.Encrypt(ctx, "", []byte("v"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Rotate(ctx, ""); err != nil {
		t.Fatal(err)
	}
	rw, err := svc.Rewrap(ctx, "", old.Ciphertext, nil)
	if err != nil || rw.ToVersion != 2 {
		t.Fatalf("Rewrap = %+v, %v", rw, err)
	}
	if _, err := svc.Retire(ctx, "", 2); !errors.Is(err, dek.ErrRetireCurrentVersion) {
		t.Fatalf("retire current: err = %v", err)
	}
	st, err := svc.Retire(ctx, "", 1)
	if err != nil || st.Versions[0].RetiredAt == nil {
		t.Fatalf("Retire = %+v, %v", st, err)
	}
}

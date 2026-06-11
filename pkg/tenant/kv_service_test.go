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

package tenant

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
)

// ---------------------------------------------------------------------------
// In-memory kvstore.KVStore for tests
// ---------------------------------------------------------------------------

type memKVStore struct {
	data map[string][]byte
	err  error // when non-nil, all operations return this error
}

func newMemKVStore() *memKVStore {
	return &memKVStore{data: make(map[string][]byte)}
}

func (m *memKVStore) Put(_ context.Context, key string, value []byte) error {
	if m.err != nil {
		return m.err
	}
	cp := make([]byte, len(value))
	copy(cp, value)
	m.data[key] = cp
	return nil
}

func (m *memKVStore) Get(_ context.Context, key string) ([]byte, error) {
	if m.err != nil {
		return nil, m.err
	}
	v, ok := m.data[key]
	if !ok {
		return nil, nil
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

func (m *memKVStore) Delete(_ context.Context, key string) error {
	if m.err != nil {
		return m.err
	}
	delete(m.data, key)
	return nil
}

func (m *memKVStore) List(_ context.Context, prefix string) ([]string, error) {
	if m.err != nil {
		return nil, m.err
	}
	var keys []string
	for k := range m.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *memKVStore) Exists(_ context.Context, key string) (bool, error) {
	if m.err != nil {
		return false, m.err
	}
	_, ok := m.data[key]
	return ok, nil
}

func (m *memKVStore) Scan(_ context.Context, prefix string, fn func(string, []byte) error) error {
	if m.err != nil {
		return m.err
	}
	for k, v := range m.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			if err := fn(k, v); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *memKVStore) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error { return nil }
func (m *memKVStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}
func (m *memKVStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}
func (m *memKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}
func (m *memKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

// Compile-time interface check.
var _ kvstore.KVStore = (*memKVStore)(nil)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

var errKVFault = errors.New("kvstore: injected fault")

func newKVService(t *testing.T) TenantServicer {
	t.Helper()
	svc, err := NewKVTenantService(newMemKVStore(), slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	return svc
}

// ---------------------------------------------------------------------------
// NewKVTenantService constructor tests
// ---------------------------------------------------------------------------

func TestNewKVTenantService_NilStore(t *testing.T) {
	_, err := NewKVTenantService(nil, slog.Default())
	if !errors.Is(err, ErrNilStore) {
		t.Fatalf("want ErrNilStore, got %v", err)
	}
}

func TestNewKVTenantService_NilLogger(t *testing.T) {
	svc, err := NewKVTenantService(newMemKVStore(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
}

// ---------------------------------------------------------------------------
// CreateTenant
// ---------------------------------------------------------------------------

func TestKVCreateTenant_Success(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	info, err := svc.CreateTenant(ctx, "acme", "Acme Corp", "")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if info.ID != "acme" {
		t.Fatalf("id = %q, want %q", info.ID, "acme")
	}
	if info.Name != "Acme Corp" {
		t.Fatalf("name = %q, want %q", info.Name, "Acme Corp")
	}
	if info.CreatedAt.IsZero() {
		t.Fatal("CreatedAt is zero")
	}
}

func TestKVCreateTenant_EmptyID(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	_, err := svc.CreateTenant(ctx, "", "Acme Corp", "")
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Fatalf("want ErrEmptyTenantID, got %v", err)
	}
}

func TestKVCreateTenant_EmptyName(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	_, err := svc.CreateTenant(ctx, "acme", "", "")
	if !errors.Is(err, ErrEmptyTenantName) {
		t.Fatalf("want ErrEmptyTenantName, got %v", err)
	}
}

func TestKVCreateTenant_AlreadyExists(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	if _, err := svc.CreateTenant(ctx, "acme", "Acme Corp", ""); err != nil {
		t.Fatalf("first CreateTenant: %v", err)
	}
	_, err := svc.CreateTenant(ctx, "acme", "Duplicate", "")
	if !errors.Is(err, ErrTenantAlreadyExists) {
		t.Fatalf("want ErrTenantAlreadyExists, got %v", err)
	}
}

func TestKVCreateTenant_StoreError(t *testing.T) {
	ctx := context.Background()
	store := newMemKVStore()
	store.err = errKVFault
	svc, _ := NewKVTenantService(store, slog.Default())

	_, err := svc.CreateTenant(ctx, "acme", "Acme Corp", "")
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("want *StoreError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// GetTenant
// ---------------------------------------------------------------------------

func TestKVGetTenant_Success(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	if _, err := svc.CreateTenant(ctx, "acme", "Acme Corp", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	info, err := svc.GetTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if info.ID != "acme" {
		t.Fatalf("id = %q, want %q", info.ID, "acme")
	}
}

func TestKVGetTenant_EmptyID(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	_, err := svc.GetTenant(ctx, "")
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Fatalf("want ErrEmptyTenantID, got %v", err)
	}
}

func TestKVGetTenant_NotFound(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	_, err := svc.GetTenant(ctx, "missing")
	if !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("want ErrTenantNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ListTenants
// ---------------------------------------------------------------------------

func TestKVListTenants_Empty(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	tenants, err := svc.ListTenants(ctx)
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(tenants) != 0 {
		t.Fatalf("want 0 tenants, got %d", len(tenants))
	}
}

func TestKVListTenants_Multiple(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	if _, err := svc.CreateTenant(ctx, "alpha", "Alpha", ""); err != nil {
		t.Fatalf("CreateTenant alpha: %v", err)
	}
	if _, err := svc.CreateTenant(ctx, "beta", "Beta", ""); err != nil {
		t.Fatalf("CreateTenant beta: %v", err)
	}

	tenants, err := svc.ListTenants(ctx)
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(tenants) != 2 {
		t.Fatalf("want 2 tenants, got %d", len(tenants))
	}
}

func TestKVListTenants_StoreError(t *testing.T) {
	ctx := context.Background()
	store := newMemKVStore()
	svc, _ := NewKVTenantService(store, slog.Default())

	store.err = errKVFault

	_, err := svc.ListTenants(ctx)
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("want *StoreError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// DeleteTenant
// ---------------------------------------------------------------------------

func TestKVDeleteTenant_Success(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	if _, err := svc.CreateTenant(ctx, "acme", "Acme Corp", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	if err := svc.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}

	_, err := svc.GetTenant(ctx, "acme")
	if !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("want ErrTenantNotFound after delete, got %v", err)
	}
}

func TestKVDeleteTenant_EmptyID(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	err := svc.DeleteTenant(ctx, "")
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Fatalf("want ErrEmptyTenantID, got %v", err)
	}
}

func TestKVDeleteTenant_NotFound(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	err := svc.DeleteTenant(ctx, "missing")
	if !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("want ErrTenantNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// UpdateQuotas
// ---------------------------------------------------------------------------

func TestKVUpdateQuotas_Success(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	if _, err := svc.CreateTenant(ctx, "acme", "Acme Corp", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	q := &quota.Quotas{MaxKeys: 1000}
	info, err := svc.UpdateQuotas(ctx, "acme", q)
	if err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}
	if info.Quotas == nil || info.Quotas.MaxKeys != 1000 {
		t.Fatalf("want MaxKeys=1000, got %v", info.Quotas)
	}
}

func TestKVUpdateQuotas_EmptyID(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	_, err := svc.UpdateQuotas(ctx, "", &quota.Quotas{})
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Fatalf("want ErrEmptyTenantID, got %v", err)
	}
}

func TestKVUpdateQuotas_NilQuotas(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	_, err := svc.UpdateQuotas(ctx, "acme", nil)
	if !errors.Is(err, ErrNilQuotas) {
		t.Fatalf("want ErrNilQuotas, got %v", err)
	}
}

func TestKVUpdateQuotas_NotFound(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	_, err := svc.UpdateQuotas(ctx, "missing", &quota.Quotas{MaxKeys: 10})
	if !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("want ErrTenantNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Barrier operations (no-op when seal is disabled)
// ---------------------------------------------------------------------------

func TestKVTenantBarrierInit_NotEnabled(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	_, err := svc.TenantBarrierInit(ctx, "acme", "pw", 2, 3)
	if !errors.Is(err, ErrBarrierNotEnabled) {
		t.Fatalf("want ErrBarrierNotEnabled, got %v", err)
	}
}

func TestKVTenantBarrierUnseal_NotEnabled(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	err := svc.TenantBarrierUnseal(ctx, "acme", []byte("key"))
	if !errors.Is(err, ErrBarrierNotEnabled) {
		t.Fatalf("want ErrBarrierNotEnabled, got %v", err)
	}
}

// TestKVTenantService_CreateTenant_BarrierIsolationRejected verifies that the
// no-seal KV-backed tenant service fails closed when barrier isolation is
// requested: it has no barrier registry, so accepting the request would
// persist a barrier-isolated tenant whose barrier never exists.
func TestKVTenantService_CreateTenant_BarrierIsolationRejected(t *testing.T) {
	ctx := context.Background()
	store := newMemKVStore()
	svc, err := NewKVTenantService(store, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}

	info, err := svc.CreateTenant(ctx, "vault", "Vault", IsolationBarrier)
	if !errors.Is(err, ErrBarrierIsolationUnavailable) {
		t.Fatalf("want ErrBarrierIsolationUnavailable, got %v", err)
	}
	if info != nil {
		t.Fatalf("expected nil TenantInfo on rejection, got %+v", info)
	}

	// Fail-closed contract: nothing must be persisted for the rejected tenant.
	key := metadataPrefix + "vault"
	data, getErr := store.Get(ctx, key)
	if getErr != nil {
		t.Fatalf("store.Get: %v", getErr)
	}
	if data != nil {
		t.Fatalf("barrier-isolated tenant must not be persisted in no-seal mode")
	}
}

// TestKVTenantService_CreateTenant_BarrierIsolationLeavesPrefixUnaffected
// confirms the rejection is scoped to barrier isolation only: prefix and
// default (empty) isolation continue to succeed.
func TestKVTenantService_CreateTenant_BarrierIsolationLeavesPrefixUnaffected(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	if _, err := svc.CreateTenant(ctx, "p1", "Prefix One", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant(prefix): %v", err)
	}
	if _, err := svc.CreateTenant(ctx, "p2", "Default", ""); err != nil {
		t.Fatalf("CreateTenant(default): %v", err)
	}
}

// TestKVTenantService_UpdateTenant_BarrierIsolationRejected verifies the
// symmetric guard: an existing tenant cannot be upgraded to barrier isolation
// in no-seal mode, and the upgrade attempt does not mutate stored metadata.
func TestKVTenantService_UpdateTenant_BarrierIsolationRejected(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	if _, err := svc.CreateTenant(ctx, "upg", "Upgrade", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	info, err := svc.UpdateTenant(ctx, "upg", TenantPatch{IsolationType: IsolationBarrier})
	if !errors.Is(err, ErrBarrierIsolationUnavailable) {
		t.Fatalf("want ErrBarrierIsolationUnavailable, got %v", err)
	}
	if info != nil {
		t.Fatalf("expected nil TenantInfo on rejection, got %+v", info)
	}

	// The stored tenant must still be prefix-isolated (unchanged).
	stored, getErr := svc.GetTenant(ctx, "upg")
	if getErr != nil {
		t.Fatalf("GetTenant: %v", getErr)
	}
	if stored.IsolationType != IsolationPrefix {
		t.Fatalf("isolation type = %q, want %q (unchanged)", stored.IsolationType, IsolationPrefix)
	}
}

// ---------------------------------------------------------------------------
// IsolationType defaults and persistence
// ---------------------------------------------------------------------------

func TestKVCreateTenant_DefaultIsolationType(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	// Empty isolation type should default to IsolationPrefix (barrier is opt-in).
	info, err := svc.CreateTenant(ctx, "iso-default", "Default ISO", "")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if info.EffectiveIsolationType() != IsolationPrefix {
		t.Fatalf("isolation type = %q, want %q", info.EffectiveIsolationType(), IsolationPrefix)
	}
}

func TestKVCreateTenant_ExplicitPrefixIsolationType(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	info, err := svc.CreateTenant(ctx, "iso-prefix", "Prefix ISO", IsolationPrefix)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if info.IsolationType != IsolationPrefix {
		t.Fatalf("isolation type = %q, want %q", info.IsolationType, IsolationPrefix)
	}
}

func TestKVCreateTenant_IsolationTypeRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	_, err := svc.CreateTenant(ctx, "rt", "Round Trip", IsolationPrefix)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Retrieve and verify the isolation type persisted correctly.
	info, err := svc.GetTenant(ctx, "rt")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if info.IsolationType != IsolationPrefix {
		t.Fatalf("isolation type = %q, want %q", info.IsolationType, IsolationPrefix)
	}
}

func TestKVCreateTenant_MissingIsolationTypeBackCompat(t *testing.T) {
	// Simulate an old tenant record with no isolation_type field by
	// unmarshalling a minimal JSON blob and verifying the default.
	info := &TenantInfo{ID: "old", Name: "Old Tenant"}
	if info.EffectiveIsolationType() != IsolationPrefix {
		t.Fatalf("EffectiveIsolationType() = %q, want %q for zero value (legacy -> shared store)", info.EffectiveIsolationType(), IsolationPrefix)
	}
}

// ---------------------------------------------------------------------------
// UpdateTenant
// ---------------------------------------------------------------------------

func TestKVUpdateTenant_Name(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	if _, err := svc.CreateTenant(ctx, "upd", "Original", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	info, err := svc.UpdateTenant(ctx, "upd", TenantPatch{Name: "Updated"})
	if err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}
	if info.Name != "Updated" {
		t.Fatalf("name = %q, want %q", info.Name, "Updated")
	}
}

func TestKVUpdateTenant_IsolationType(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	// Barrier creation is rejected in no-seal mode (see
	// TestKVCreateTenant_BarrierIsolationRejected); create a prefix tenant
	// and verify a prefix->prefix patch round-trips through UpdateTenant.
	if _, err := svc.CreateTenant(ctx, "iso-upd", "ISO Update", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	info, err := svc.UpdateTenant(ctx, "iso-upd", TenantPatch{IsolationType: IsolationPrefix})
	if err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}
	if info.IsolationType != IsolationPrefix {
		t.Fatalf("isolation type = %q, want %q", info.IsolationType, IsolationPrefix)
	}
}

func TestKVUpdateTenant_NotFound(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	_, err := svc.UpdateTenant(ctx, "ghost", TenantPatch{Name: "Ghost"})
	if !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("want ErrTenantNotFound, got %v", err)
	}
}

func TestKVUpdateTenant_EmptyID(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	_, err := svc.UpdateTenant(ctx, "", TenantPatch{Name: "No ID"})
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Fatalf("want ErrEmptyTenantID, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Reserved tenant id rejection (kv-service path)
// ---------------------------------------------------------------------------

func TestKVCreateTenant_RejectsSystemID(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	_, err := svc.CreateTenant(ctx, SystemTenantID, "System", "")
	var rerr *ReservedTenantIDError
	if !errors.As(err, &rerr) {
		t.Fatalf("expected *ReservedTenantIDError, got %T: %v", err, err)
	}
}

func TestKVCreateTenant_RejectsLegacyDefaultID(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	_, err := svc.CreateTenant(ctx, "default", "Default", "")
	var rerr *ReservedTenantIDError
	if !errors.As(err, &rerr) {
		t.Fatalf("expected *ReservedTenantIDError, got %T: %v", err, err)
	}
}

func TestKVCreateTenant_RejectsUnderscorePrefix(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	_, err := svc.CreateTenant(ctx, "_special", "Underscore", "")
	var rerr *ReservedTenantIDError
	if !errors.As(err, &rerr) {
		t.Fatalf("expected *ReservedTenantIDError, got %T: %v", err, err)
	}
}

func TestKVCreateTenant_RejectsColonInfix(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	_, err := svc.CreateTenant(ctx, "acme:prod", "Colon", "")
	var rerr *ReservedTenantIDError
	if !errors.As(err, &rerr) {
		t.Fatalf("expected *ReservedTenantIDError, got %T: %v", err, err)
	}
}

func TestKVCreateTenant_InternalCallAllowsSystemID(t *testing.T) {
	ctx := WithInternalCall(context.Background())
	svc := newKVService(t)

	info, err := svc.CreateTenant(ctx, SystemTenantID, "System", "")
	if err != nil {
		t.Fatalf("internal-call CreateTenant(system): %v", err)
	}
	if info.ID != SystemTenantID {
		t.Errorf("ID = %q, want %q", info.ID, SystemTenantID)
	}
}

// ---------------------------------------------------------------------------
// kvTenantService — new barrier officer/claim/seal-vote stubs
// ---------------------------------------------------------------------------

func newKVServiceForBarrierTest(t *testing.T) TenantServicer {
	t.Helper()
	store := newMemKVStore()
	svc, err := NewKVTenantService(store, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	return svc
}

func TestKVTenantBarrierSeal_NotEnabled2(t *testing.T) {
	svc := newKVServiceForBarrierTest(t)
	if err := svc.TenantBarrierSeal(context.Background(), "t"); !errors.Is(err, ErrBarrierNotEnabled) {
		t.Errorf("expected ErrBarrierNotEnabled, got %v", err)
	}
}

func TestKVTenantBarrierStatus_NotEnabled2(t *testing.T) {
	svc := newKVServiceForBarrierTest(t)
	if _, err := svc.TenantBarrierStatus(context.Background(), "t"); !errors.Is(err, ErrBarrierNotEnabled) {
		t.Errorf("expected ErrBarrierNotEnabled, got %v", err)
	}
}

func TestKVTenantBarrierUnsealShare_NotEnabled(t *testing.T) {
	svc := newKVServiceForBarrierTest(t)
	if _, err := svc.TenantBarrierUnsealShare(context.Background(), "t", "share"); !errors.Is(err, ErrBarrierNotEnabled) {
		t.Errorf("expected ErrBarrierNotEnabled, got %v", err)
	}
}

func TestKVTenantBarrierOfficerRegister_NotEnabled(t *testing.T) {
	svc := newKVServiceForBarrierTest(t)
	_, err := svc.TenantBarrierOfficerRegister(context.Background(), "t", nil)
	if !errors.Is(err, ErrBarrierNotEnabled) {
		t.Errorf("expected ErrBarrierNotEnabled, got %v", err)
	}
}

func TestKVTenantBarrierOfficerList_NotEnabled(t *testing.T) {
	svc := newKVServiceForBarrierTest(t)
	_, err := svc.TenantBarrierOfficerList(context.Background(), "t")
	if !errors.Is(err, ErrBarrierNotEnabled) {
		t.Errorf("expected ErrBarrierNotEnabled, got %v", err)
	}
}

func TestKVTenantBarrierShareClaimBegin_NotEnabled(t *testing.T) {
	svc := newKVServiceForBarrierTest(t)
	_, err := svc.TenantBarrierShareClaimBegin(context.Background(), "t", "u", "thumb")
	if !errors.Is(err, ErrBarrierNotEnabled) {
		t.Errorf("expected ErrBarrierNotEnabled, got %v", err)
	}
}

func TestKVTenantBarrierShareClaimComplete_NotEnabled(t *testing.T) {
	svc := newKVServiceForBarrierTest(t)
	_, err := svc.TenantBarrierShareClaimComplete(context.Background(), "t", "u", "nonce", []byte("sig"), "thumb")
	if !errors.Is(err, ErrBarrierNotEnabled) {
		t.Errorf("expected ErrBarrierNotEnabled, got %v", err)
	}
}

func TestKVTenantBarrierOfficerClaimStatus_NotEnabled(t *testing.T) {
	svc := newKVServiceForBarrierTest(t)
	_, err := svc.TenantBarrierOfficerClaimStatus(context.Background(), "t")
	if !errors.Is(err, ErrBarrierNotEnabled) {
		t.Errorf("expected ErrBarrierNotEnabled, got %v", err)
	}
}

func TestKVTenantBarrierShamirSealVote_NotEnabled(t *testing.T) {
	svc := newKVServiceForBarrierTest(t)
	_, err := svc.TenantBarrierShamirSealVote(context.Background(), "t", "share")
	if !errors.Is(err, ErrBarrierNotEnabled) {
		t.Errorf("expected ErrBarrierNotEnabled, got %v", err)
	}
}

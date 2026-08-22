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

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// newTestAccountant builds a quota.Accountant backed by an in-memory persisted
// counter keyed by tenant. The returned map is the durable CurrentStorageBytes.
func newTestAccountant(t *testing.T, base map[string]int64) (*quota.Accountant, map[string]int64) {
	t.Helper()
	persisted := map[string]int64{}
	for k, v := range base {
		persisted[k] = v
	}
	acct, err := quota.NewAccountant(quota.AccountantConfig{
		Quotas: func(_ context.Context, tenantID string) (*quota.Quotas, error) {
			return &quota.Quotas{CurrentStorageBytes: persisted[tenantID]}, nil
		},
		Flush: func(_ context.Context, tenantID string, delta int64) error {
			persisted[tenantID] += delta
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewAccountant: %v", err)
	}
	return acct, persisted
}

func TestQuotaKVService_PutAccountsAndEnforces(t *testing.T) {
	inner := newMockKVService()
	acct, persisted := newTestAccountant(t, nil)
	kv := newQuotaKVService(inner, acct, "t1", 100)
	ctx := context.Background()

	if err := kv.Put(ctx, "a", make([]byte, 60)); err != nil {
		t.Fatalf("put 60: %v", err)
	}
	if got := acct.Usage("t1"); got != 60 {
		t.Fatalf("usage = %d, want 60", got)
	}

	// A write that would exceed the 100-byte limit is rejected with ExceededError
	// and never reaches the inner store.
	err := kv.Put(ctx, "b", make([]byte, 50))
	var ee *quota.ExceededError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *quota.ExceededError, got %v", err)
	}
	if _, gerr := inner.Get(ctx, "b"); gerr == nil {
		t.Fatal("rejected write must not be committed to the inner store")
	}
	if got := acct.Usage("t1"); got != 60 {
		t.Fatalf("usage after rejection = %d, want 60", got)
	}

	// A write that fits succeeds.
	if err := kv.Put(ctx, "c", make([]byte, 40)); err != nil {
		t.Fatalf("put 40 (to limit): %v", err)
	}
	acct.Flush(ctx)
	if got := persisted["t1"]; got != 100 {
		t.Fatalf("persisted = %d, want 100", got)
	}
}

func TestQuotaKVService_DeleteSubtracts(t *testing.T) {
	inner := newMockKVService()
	acct, persisted := newTestAccountant(t, nil)
	kv := newQuotaKVService(inner, acct, "t1", 1000)
	ctx := context.Background()

	if err := kv.Put(ctx, "a", make([]byte, 200)); err != nil {
		t.Fatalf("put: %v", err)
	}
	if got := acct.Usage("t1"); got != 200 {
		t.Fatalf("usage = %d, want 200", got)
	}
	if err := kv.Delete(ctx, "a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("usage after delete = %d, want 0", got)
	}
	acct.Flush(ctx)
	if got := persisted["t1"]; got != 0 {
		t.Fatalf("persisted = %d, want 0", got)
	}
}

// staleLaggingKVService models a store whose local (stale) read lags the
// linearizable read: the just-committed value is visible via Get but not yet via
// GetStale. It is the failure mode behind finding M-2 case 2 — a delete that
// sizes the freed bytes from a stale read frees zero and inflates usage forever.
type staleLaggingKVService struct {
	*mockKVService
}

func (s *staleLaggingKVService) GetStale(_ context.Context, key string) ([]byte, error) {
	return nil, &service.KeyNotFoundError{Key: key}
}

// TestQuotaKVService_DeleteUsesLinearizableRead asserts the delete path sizes the
// freed bytes via the linearizable Get, so deleting a just-written key whose
// value the stale read cannot yet see still frees the correct amount and leaves
// usage at zero (not inflated).
func TestQuotaKVService_DeleteUsesLinearizableRead(t *testing.T) {
	inner := &staleLaggingKVService{mockKVService: newMockKVService()}
	acct, persisted := newTestAccountant(t, nil)
	kv := newQuotaKVService(inner, acct, "t1", 1000)
	ctx := context.Background()

	if err := kv.Put(ctx, "a", make([]byte, 200)); err != nil {
		t.Fatalf("put: %v", err)
	}
	if got := acct.Usage("t1"); got != 200 {
		t.Fatalf("usage = %d, want 200", got)
	}
	// With a stale read this delete would free 0 and usage would stay at 200.
	if err := kv.Delete(ctx, "a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("usage after delete = %d, want 0 (linearizable read frees full value)", got)
	}
	acct.Flush(ctx)
	if got := persisted["t1"]; got != 0 {
		t.Fatalf("persisted = %d, want 0", got)
	}
}

// TestQuotaKVService_OverDeleteNeverNegative pins the effective-usage floor on
// the server path: even when more bytes are subtracted than the base reflects
// (e.g. a small write followed by deletes of larger out-of-band values), usage
// never goes negative.
func TestQuotaKVService_OverDeleteNeverNegative(t *testing.T) {
	inner := newMockKVService()
	acct, _ := newTestAccountant(t, nil)
	kv := newQuotaKVService(inner, acct, "t1", 1000)
	ctx := context.Background()

	if err := kv.Put(ctx, "small", make([]byte, 10)); err != nil {
		t.Fatalf("put: %v", err)
	}
	// Seed a larger value out-of-band (no Reserve accounted it), then delete it:
	// the freed bytes exceed the accounted usage.
	if err := inner.Put(ctx, "big", make([]byte, 500)); err != nil {
		t.Fatalf("seed put: %v", err)
	}
	if err := kv.Delete(ctx, "big"); err != nil {
		t.Fatalf("delete big: %v", err)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("usage = %d, want 0 (floored, never negative)", got)
	}
}

func TestQuotaKVService_DeleteMissingKeyNoUnderflow(t *testing.T) {
	inner := newMockKVService()
	acct, _ := newTestAccountant(t, nil)
	kv := newQuotaKVService(inner, acct, "t1", 1000)
	ctx := context.Background()
	if err := kv.Delete(ctx, "absent"); err != nil {
		t.Fatalf("delete absent: %v", err)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("usage = %d, want 0", got)
	}
}

func TestApplyQuota_InstallsForLimitedTenant(t *testing.T) {
	inner := newMockKVService()
	acct, _ := newTestAccountant(t, nil)
	r := &restServiceRegistry{accountant: acct, systemTenant: tenant.SystemTenantID}

	svcs := &DatabaseServices{KV: inner}
	info := &tenant.TenantInfo{ID: "t1", Quotas: &quota.Quotas{MaxStorageBytes: 100}}
	out := r.applyQuota(svcs, "t1", info)

	if _, ok := out.KV.(*quotaKVService); !ok {
		t.Fatalf("expected KV wrapped with *quotaKVService, got %T", out.KV)
	}
}

func TestApplyQuota_SkipsSystemTenant(t *testing.T) {
	inner := newMockKVService()
	acct, _ := newTestAccountant(t, nil)
	r := &restServiceRegistry{accountant: acct, systemTenant: tenant.SystemTenantID}

	svcs := &DatabaseServices{KV: inner}
	info := &tenant.TenantInfo{ID: tenant.SystemTenantID, Quotas: &quota.Quotas{MaxStorageBytes: 100}}
	out := r.applyQuota(svcs, tenant.SystemTenantID, info)

	if out != svcs {
		t.Fatal("system tenant must not be wrapped (passthrough)")
	}
}

func TestApplyQuota_SkipsUnlimitedTenant(t *testing.T) {
	inner := newMockKVService()
	acct, _ := newTestAccountant(t, nil)
	r := &restServiceRegistry{accountant: acct, systemTenant: tenant.SystemTenantID}

	svcs := &DatabaseServices{KV: inner}
	// MaxStorageBytes <= 0 means unlimited -> no wrapper, hot path untouched.
	for _, limit := range []int64{0, -1} {
		info := &tenant.TenantInfo{ID: "t1", Quotas: &quota.Quotas{MaxStorageBytes: limit}}
		if out := r.applyQuota(svcs, "t1", info); out != svcs {
			t.Fatalf("limit %d: expected passthrough, got wrapped", limit)
		}
	}
}

func TestApplyQuota_SkipsWhenNoAccountant(t *testing.T) {
	inner := newMockKVService()
	r := &restServiceRegistry{systemTenant: tenant.SystemTenantID}
	svcs := &DatabaseServices{KV: inner}
	info := &tenant.TenantInfo{ID: "t1", Quotas: &quota.Quotas{MaxStorageBytes: 100}}
	if out := r.applyQuota(svcs, "t1", info); out != svcs {
		t.Fatal("nil accountant must be passthrough")
	}
}

// committedThenFailedKV is an inner store whose CompareAndSwap APPLIES the
// swap and then reports an error.
//
// That is not a contrived double: for a Raft-backed service, SyncPropose can
// commit the entry and then fail to deliver its response — a lost connection
// after a durable write. AtomicKVStore says a non-nil error leaves the outcome
// unknown, and this is what "unknown" actually looks like.
type committedThenFailedKV struct {
	*mockKVService
	err error
}

func (m *committedThenFailedKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	// Apply first, then fail: the write is durable, the answer is lost.
	if _, err := m.mockKVService.CompareAndSwap(ctx, key, expected, next); err != nil {
		return false, err
	}
	return false, m.err
}

// A swap whose outcome is UNKNOWN keeps its reservation.
//
// Releasing on error would drop the accounting for a value that is stored, and
// the tenant's usage would sit permanently below what they hold — every later
// write measured against a limit that no longer bounds anything. The
// conservative direction is to keep the reservation (go-qrdb#56).
func TestQuotaKVService_CompareAndSwap_AmbiguousErrorKeepsReservation(t *testing.T) {
	lost := errors.New("propose committed but the response was lost")
	inner := &committedThenFailedKV{mockKVService: newMockKVService(), err: lost}
	acct, _ := newTestAccountant(t, nil)
	kv := newQuotaKVService(inner, acct, "t1", 100)
	ctx := context.Background()

	if err := kv.Put(ctx, "k", make([]byte, 10)); err != nil {
		t.Fatalf("seed put: %v", err)
	}
	if got := acct.Usage("t1"); got != 10 {
		t.Fatalf("usage after seed = %d, want 10", got)
	}

	// Grow 10 -> 40: reserves 30, the swap commits, the answer is lost.
	swapped, err := kv.CompareAndSwap(ctx, "k", make([]byte, 10), make([]byte, 40))
	if !errors.Is(err, lost) {
		t.Fatalf("CompareAndSwap error = %v, want the inner error", err)
	}
	if swapped {
		t.Error("an ambiguous outcome must not be reported as a completed swap")
	}

	// The value IS stored, so the growth must still be accounted.
	if got := len(inner.store["k"]); got != 40 {
		t.Fatalf("inner value = %d bytes, want the committed 40", got)
	}
	if got := acct.Usage("t1"); got != 40 {
		t.Fatalf("usage = %d, want 40 — the growth was released for a value that "+
			"is stored, so the tenant can now exceed their limit", got)
	}

	// The limit still bounds: 40 held, 100 limit, a 70-byte write must be refused.
	err = kv.Put(ctx, "b", make([]byte, 70))
	var ee *quota.ExceededError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *quota.ExceededError once usage is correct, got %v", err)
	}
}

// A swap DEFINITIVELY refused releases its reservation.
//
// The counterpart to the ambiguous case: a guard that was evaluated and said
// no wrote nothing, so holding the growth would leak headroom on every
// contended swap.
func TestQuotaKVService_CompareAndSwap_RefusedReleasesReservation(t *testing.T) {
	inner := newMockKVService()
	acct, _ := newTestAccountant(t, nil)
	kv := newQuotaKVService(inner, acct, "t1", 100)
	ctx := context.Background()

	if err := kv.Put(ctx, "k", make([]byte, 10)); err != nil {
		t.Fatalf("seed put: %v", err)
	}

	// Guard does not match the stored value: refused, nothing written.
	swapped, err := kv.CompareAndSwap(ctx, "k", make([]byte, 25), make([]byte, 40))
	if err != nil || swapped {
		t.Fatalf("CompareAndSwap = (%v, %v), want (false, nil)", swapped, err)
	}
	if got := acct.Usage("t1"); got != 10 {
		t.Fatalf("usage = %d, want 10 — a refused swap must release its reservation", got)
	}
}

// A swap that SUCCEEDS accounts the delta in both directions.
func TestQuotaKVService_CompareAndSwap_AccountsTheDelta(t *testing.T) {
	inner := newMockKVService()
	acct, _ := newTestAccountant(t, nil)
	kv := newQuotaKVService(inner, acct, "t1", 100)
	ctx := context.Background()

	if err := kv.Put(ctx, "k", make([]byte, 10)); err != nil {
		t.Fatalf("seed put: %v", err)
	}

	// Grow 10 -> 40.
	if swapped, err := kv.CompareAndSwap(ctx, "k", make([]byte, 10), make([]byte, 40)); err != nil || !swapped {
		t.Fatalf("grow = (%v, %v), want (true, nil)", swapped, err)
	}
	if got := acct.Usage("t1"); got != 40 {
		t.Fatalf("usage after growth = %d, want 40", got)
	}

	// Shrink 40 -> 15.
	if swapped, err := kv.CompareAndSwap(ctx, "k", make([]byte, 40), make([]byte, 15)); err != nil || !swapped {
		t.Fatalf("shrink = (%v, %v), want (true, nil)", swapped, err)
	}
	if got := acct.Usage("t1"); got != 15 {
		t.Fatalf("usage after shrink = %d, want 15", got)
	}
}

// committedThenFailedWrites applies a write and THEN reports an error, for the
// unconditional and conditional put paths.
//
// The same shape as committedThenFailedKV above, which covers the swap: a Raft
// proposal that commits and then loses its response. Separate rather than
// merged so each test names the verb it is about.
type committedThenFailedWrites struct {
	*mockKVService
	err error
}

func (m *committedThenFailedWrites) Put(ctx context.Context, key string, value []byte) error {
	_ = m.mockKVService.Put(ctx, key, value)
	return m.err
}

func (m *committedThenFailedWrites) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if _, err := m.mockKVService.PutIfAbsent(ctx, key, value); err != nil {
		return false, err
	}
	return false, m.err
}

func (m *committedThenFailedWrites) PutWithMode(ctx context.Context, key string, value []byte, mode writemode.Mode) error {
	_ = m.mockKVService.Put(ctx, key, value)
	return m.err
}

// PutEntity stores under the entity id, because service.KVService.PutEntity
// delegates to the same Raft-backed Put — the entity path commits and loses its
// response exactly as the plain verbs do.
func (m *committedThenFailedWrites) PutEntity(ctx context.Context, req *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	_ = m.mockKVService.Put(ctx, req.ID, req.Data)
	return nil, m.err
}

// A write whose outcome is UNKNOWN keeps its reservation, on every verb.
//
// Driven as a table because the requirement is that the verbs AGREE: this
// defect was fixed for CompareAndSwap in go-qrdb#56 and left in place on its
// three siblings, which is how one verb ends up behaving unlike the rest
// (go-qrdb#90).
func TestQuotaKVService_AmbiguousWriteKeepsReservation(t *testing.T) {
	lost := errors.New("propose committed but the response was lost")

	verbs := map[string]func(context.Context, *quotaKVService) error{
		"Put": func(ctx context.Context, kv *quotaKVService) error {
			return kv.Put(ctx, "k", make([]byte, 40))
		},
		"PutIfAbsent": func(ctx context.Context, kv *quotaKVService) error {
			_, err := kv.PutIfAbsent(ctx, "k", make([]byte, 40))
			return err
		},
		"PutWithMode": func(ctx context.Context, kv *quotaKVService) error {
			return kv.PutWithMode(ctx, "k", make([]byte, 40), writemode.Sync)
		},
		"PutEntity": func(ctx context.Context, kv *quotaKVService) error {
			_, err := kv.PutEntity(ctx, &service.PutEntityRequest{ID: "k", Data: make([]byte, 40)})
			return err
		},
	}

	for name, write := range verbs {
		t.Run(name, func(t *testing.T) {
			inner := &committedThenFailedWrites{mockKVService: newMockKVService(), err: lost}
			acct, _ := newTestAccountant(t, nil)
			kv := newQuotaKVService(inner, acct, "t1", 100)
			ctx := context.Background()

			if err := write(ctx, kv); !errors.Is(err, lost) {
				t.Fatalf("%s error = %v, want the inner error", name, err)
			}

			// The value IS stored, so its bytes must stay accounted.
			if got := len(inner.store["k"]); got != 40 {
				t.Fatalf("inner value = %d bytes, want the committed 40", got)
			}
			if got := acct.Usage("t1"); got != 40 {
				t.Fatalf("usage = %d, want 40 — the reservation was released for a "+
					"value that is stored, so the tenant can now exceed their limit", got)
			}

			// The limit still bounds: 40 held against 100, so 70 must be refused.
			var ee *quota.ExceededError
			if err := kv.Put(ctx, "b", make([]byte, 70)); !errors.As(err, &ee) {
				t.Fatalf("expected *quota.ExceededError once usage is correct, got %v", err)
			}
		})
	}
}

// A DEFINITIVELY declined PutIfAbsent still releases its reservation.
//
// The counterpart: a guard that was evaluated and said no wrote nothing, so
// holding its bytes would leak headroom on every contended claim. This is the
// case the fix must not break while closing the ambiguous one.
func TestQuotaKVService_DeclinedPutIfAbsentReleasesReservation(t *testing.T) {
	inner := newMockKVService()
	acct, _ := newTestAccountant(t, nil)
	kv := newQuotaKVService(inner, acct, "t1", 100)
	ctx := context.Background()

	if err := kv.Put(ctx, "k", make([]byte, 10)); err != nil {
		t.Fatalf("seed put: %v", err)
	}

	stored, err := kv.PutIfAbsent(ctx, "k", make([]byte, 40))
	if err != nil || stored {
		t.Fatalf("PutIfAbsent = (%v, %v), want (false, nil)", stored, err)
	}
	if got := acct.Usage("t1"); got != 10 {
		t.Fatalf("usage = %d, want 10 — a declined conditional write must release "+
			"its reservation", got)
	}
}

// rejectingKV refuses every write with a DEFINITIVE error — one raised before
// anything could be proposed, so nothing was stored.
type rejectingKV struct {
	*mockKVService
	err error
}

func (m *rejectingKV) Put(context.Context, string, []byte) error { return m.err }

func (m *rejectingKV) PutIfAbsent(context.Context, string, []byte) (bool, error) {
	return false, m.err
}

func (m *rejectingKV) PutWithMode(context.Context, string, []byte, writemode.Mode) error {
	return m.err
}

func (m *rejectingKV) PutEntity(context.Context, *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	return nil, m.err
}

// A DEFINITIVELY rejected write gives its reservation back.
//
// Retaining on every error is the opposite defect to releasing on every error,
// and it is the more dangerous one for a PREFIX tenant: the reconciler repairs
// volume tenants from real storage, so a prefix tenant's usage only ever
// climbs. A caller sending invalid requests in a loop would exhaust the quota
// without writing a byte — a denial of service built out of a safety measure
// (go-qrdb#90 round 2).
//
// Each error here is one the service raises before reaching consensus, or one
// a state machine returns having applied nothing.
func TestQuotaKVService_DefinitivelyRejectedWriteReleasesReservation(t *testing.T) {
	definitive := map[string]error{
		"entity type validation": &service.EntityEmptyTypeError{},
		"encode failure":         &service.EncodeRequestError{Err: errors.New("too long")},
		"not leader":             &service.NotLeaderError{ShardID: 1, LeaderID: 2},
		"subsystem absent":       &service.SMNotConfiguredError{Message: "mvcc"},
		"atomic write refused":   kvstore.ErrAtomicWriteUnsupported,
	}

	writes := map[string]func(context.Context, *quotaKVService) error{
		"Put": func(ctx context.Context, kv *quotaKVService) error {
			return kv.Put(ctx, "k", make([]byte, 40))
		},
		"PutIfAbsent": func(ctx context.Context, kv *quotaKVService) error {
			_, err := kv.PutIfAbsent(ctx, "k", make([]byte, 40))
			return err
		},
		"PutWithMode": func(ctx context.Context, kv *quotaKVService) error {
			return kv.PutWithMode(ctx, "k", make([]byte, 40), writemode.Sync)
		},
		"PutEntity": func(ctx context.Context, kv *quotaKVService) error {
			_, err := kv.PutEntity(ctx, &service.PutEntityRequest{ID: "k", Data: make([]byte, 40)})
			return err
		},
	}

	for errName, rejection := range definitive {
		for verb, write := range writes {
			t.Run(errName+"/"+verb, func(t *testing.T) {
				inner := &rejectingKV{mockKVService: newMockKVService(), err: rejection}
				acct, _ := newTestAccountant(t, nil)
				kv := newQuotaKVService(inner, acct, "t1", 100)
				ctx := context.Background()

				if err := write(ctx, kv); err == nil {
					t.Fatalf("%s: expected the rejection to propagate", verb)
				}

				if got := acct.Usage("t1"); got != 0 {
					t.Fatalf("usage = %d, want 0 — the write was definitively rejected "+
						"and stored nothing, so holding its bytes leaks quota that a "+
						"prefix tenant never gets back", got)
				}
			})
		}
	}
}

// Repeated rejections must not accumulate, which is the denial of service.
func TestQuotaKVService_RepeatedRejectionsDoNotExhaustTheQuota(t *testing.T) {
	inner := &rejectingKV{mockKVService: newMockKVService(), err: &service.EntityEmptyTypeError{}}
	acct, _ := newTestAccountant(t, nil)
	kv := newQuotaKVService(inner, acct, "t1", 100)
	ctx := context.Background()

	// Forty rejected 40-byte writes: 1600 bytes against a 100-byte limit if
	// each one leaked.
	for i := 0; i < 40; i++ {
		if _, err := kv.PutEntity(ctx, &service.PutEntityRequest{ID: "k", Data: make([]byte, 40)}); err == nil {
			t.Fatal("expected the rejection to propagate")
		}
	}

	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("usage after 40 rejected writes = %d, want 0", got)
	}

	// The tenant's quota is still usable, which is the property under attack.
	// Routed through a WORKING store on the same accountant: the rejecting
	// double would refuse this write itself and prove nothing about the quota.
	working := newQuotaKVService(newMockKVService(), acct, "t1", 100)
	if err := working.Put(ctx, "real", make([]byte, 90)); err != nil {
		t.Fatalf("a legitimate write was refused after rejected ones leaked quota: %v", err)
	}
}

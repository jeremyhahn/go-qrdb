// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
)

// readyReport is a fresh NodeReport mapping pool in state.
func readyReport(nodeID, pool string, state State) NodeReport {
	return NodeReport{
		NodeID:     nodeID,
		Pools:      map[string]PoolOnNode{pool: {Mapped: true, Health: Health{State: state}}},
		ReportedAt: time.Now(),
	}
}

// setupMoveCatalog creates pools "fast" and "bulk" on cat, reserves 200
// bytes for "acme" on "fast" and reports "bulk" ready on n1 and n2.
func setupMoveCatalog(t *testing.T, cat *Catalog, kv *fakeKV, fastProvider, bulkProvider string, bulkCapacity int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "fast", Provider: fastProvider, CapacityBytes: 1000}); err != nil {
		t.Fatalf("CreatePool fast: %v", err)
	}
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "bulk", Provider: bulkProvider, CapacityBytes: bulkCapacity}); err != nil {
		t.Fatalf("CreatePool bulk: %v", err)
	}
	if _, err := cat.Reserve(ctx, "acme", "fast", "acme", 200); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	putTestNodeReport(t, kv, readyReport("n1", "bulk", StateReady))
	putTestNodeReport(t, kv, readyReport("n2", "bulk", StateReady))
}

func newMoveTestCatalog(t *testing.T) (*Catalog, *fakeKV, *fakeTx) {
	t.Helper()
	cat, kv, tx := newTestCatalog()
	setupMoveCatalog(t, cat, kv, "", "", 1000)
	return cat, kv, tx
}

func admitAcme() MoveAdmitRequest {
	return MoveAdmitRequest{TenantID: "acme", ToPool: "bulk", ReplicaNodes: []string{"n2", "n1"}}
}

func poolReserved(t *testing.T, kv *fakeKV, name string) int64 {
	t.Helper()
	raw, err := kv.Get(context.Background(), PoolKey(name))
	if err != nil || raw == nil {
		t.Fatalf("Get pool %q: raw=%v err=%v", name, raw, err)
	}
	var p Pool
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decode pool %q: %v", name, err)
	}
	return p.ReservedBytes
}

func storedMove(t *testing.T, kv *fakeKV, tenantID string) *Move {
	t.Helper()
	raw, err := kv.Get(context.Background(), MoveKey(tenantID))
	if err != nil {
		t.Fatalf("Get move: %v", err)
	}
	if raw == nil {
		return nil
	}
	var m Move
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode move: %v", err)
	}
	return &m
}

func assertAssignedTo(t *testing.T, cat *Catalog, pool string, reserved int64) {
	t.Helper()
	a, err := cat.Resolve(context.Background(), "acme")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if a.Pool != pool || a.ReservedBytes != reserved {
		t.Fatalf("assignment = %+v, want pool %q reserved %d", a, pool, reserved)
	}
}

func TestCatalog_AdmitMove_Success(t *testing.T) {
	cat, kv, _ := newMoveTestCatalog(t)
	m, err := cat.AdmitMove(context.Background(), admitAcme())
	if err != nil {
		t.Fatalf("AdmitMove: %v", err)
	}
	if m.TenantID != "acme" || m.FromPool != "fast" || m.ToPool != "bulk" || m.State != "admitted" {
		t.Fatalf("move = %+v", m)
	}
	if !reflect.DeepEqual(m.NodesPending, []string{"n1", "n2"}) || m.NodesCopied != nil {
		t.Fatalf("nodes pending=%v copied=%v, want [n1 n2] and nil", m.NodesPending, m.NodesCopied)
	}
	if m.ReservedBytes != 200 || m.AdmittedAt.IsZero() {
		t.Fatalf("move = %+v", m)
	}
	stored := storedMove(t, kv, "acme")
	if stored == nil {
		t.Fatal("no journal at MoveKey(acme)")
	}
	if !stored.AdmittedAt.Equal(m.AdmittedAt) {
		t.Fatalf("stored AdmittedAt %v != %v", stored.AdmittedAt, m.AdmittedAt)
	}
	stored.AdmittedAt = m.AdmittedAt
	if !reflect.DeepEqual(*stored, *m) {
		t.Fatalf("stored journal %+v != returned %+v", *stored, *m)
	}
	if got := poolReserved(t, kv, "bulk"); got != 200 {
		t.Fatalf("bulk reserved = %d, want 200", got)
	}
	if got := poolReserved(t, kv, "fast"); got != 200 {
		t.Fatalf("fast reserved = %d, want 200", got)
	}
	assertAssignedTo(t, cat, "fast", 200)
}

func TestCatalog_AdmitMove_InvalidRequest(t *testing.T) {
	cat, kv, _ := newMoveTestCatalog(t)
	cases := map[string]MoveAdmitRequest{
		"empty tenant":   {ToPool: "bulk", ReplicaNodes: []string{"n1"}},
		"empty pool":     {TenantID: "acme", ReplicaNodes: []string{"n1"}},
		"no nodes":       {TenantID: "acme", ToPool: "bulk"},
		"empty node":     {TenantID: "acme", ToPool: "bulk", ReplicaNodes: []string{"n1", ""}},
		"duplicate node": {TenantID: "acme", ToPool: "bulk", ReplicaNodes: []string{"n1", "n1"}},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := cat.AdmitMove(context.Background(), req)
			var target *InvalidMoveRequestError
			if !errors.As(err, &target) {
				t.Fatalf("AdmitMove error = %v, want *InvalidMoveRequestError", err)
			}
			if storedMove(t, kv, "acme") != nil {
				t.Fatal("journal written for an invalid request")
			}
		})
	}
}

func TestCatalog_AdmitMove_NoAssignment(t *testing.T) {
	cat, _, _ := newMoveTestCatalog(t)
	req := admitAcme()
	req.TenantID = "other"
	_, err := cat.AdmitMove(context.Background(), req)
	var target *AssignmentNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("AdmitMove error = %v, want *AssignmentNotFoundError", err)
	}
}

func TestCatalog_AdmitMove_UnknownDestination(t *testing.T) {
	cat, _, _ := newMoveTestCatalog(t)
	req := admitAcme()
	req.ToPool = "missing"
	_, err := cat.AdmitMove(context.Background(), req)
	var target *PoolNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("AdmitMove error = %v, want *PoolNotFoundError", err)
	}
}

func TestCatalog_AdmitMove_SamePool(t *testing.T) {
	cat, kv, _ := newMoveTestCatalog(t)
	req := admitAcme()
	req.ToPool = "fast"
	_, err := cat.AdmitMove(context.Background(), req)
	var target *MoveConflictError
	if !errors.As(err, &target) {
		t.Fatalf("AdmitMove error = %v, want *MoveConflictError", err)
	}
	if storedMove(t, kv, "acme") != nil {
		t.Fatal("journal written for a same-pool move")
	}
}

func TestCatalog_AdmitMove_SecondAdmitRefused(t *testing.T) {
	cat, kv, _ := newMoveTestCatalog(t)
	ctx := context.Background()
	first, err := cat.AdmitMove(ctx, admitAcme())
	if err != nil {
		t.Fatalf("AdmitMove: %v", err)
	}
	_, err = cat.AdmitMove(ctx, admitAcme())
	var target *MoveConflictError
	if !errors.As(err, &target) {
		t.Fatalf("second AdmitMove error = %v, want *MoveConflictError", err)
	}
	stored := storedMove(t, kv, "acme")
	if stored == nil || !stored.AdmittedAt.Equal(first.AdmittedAt) {
		t.Fatalf("journal changed by the second admit: %+v", stored)
	}
	if got := poolReserved(t, kv, "bulk"); got != 200 {
		t.Fatalf("bulk reserved = %d, want 200 (not doubled)", got)
	}
}

func TestCatalog_AdmitMove_DestinationNotReadyOnNode(t *testing.T) {
	stale := readyReport("n2", "bulk", StateReady)
	stale.ReportedAt = time.Now().Add(-(DefaultLeaseTTL + time.Minute))
	cases := map[string]*NodeReport{
		"no report": nil,
		"stale":     &stale,
		"unmapped":  {NodeID: "n2", Pools: map[string]PoolOnNode{"fast": {Mapped: true}}, ReportedAt: time.Now()},
		"mapped false": {NodeID: "n2", Pools: map[string]PoolOnNode{"bulk": {Mapped: false}},
			ReportedAt: time.Now()},
		"locked": func() *NodeReport { r := readyReport("n2", "bulk", StateLocked); return &r }(),
	}
	for name, report := range cases {
		t.Run(name, func(t *testing.T) {
			cat, kv, _ := newTestCatalog()
			setupMoveCatalog(t, cat, kv, "", "", 1000)
			if ok, err := kv.CompareAndDelete(context.Background(), NodeReportKey("n2"), mustGet(t, kv, NodeReportKey("n2"))); err != nil || !ok {
				t.Fatalf("delete n2 report: ok=%v err=%v", ok, err)
			}
			if report != nil {
				data, err := json.Marshal(report)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				if err := kv.Put(context.Background(), NodeReportKey("n2"), data); err != nil {
					t.Fatalf("Put: %v", err)
				}
			}
			_, err := cat.AdmitMove(context.Background(), admitAcme())
			var target *MoveConflictError
			if !errors.As(err, &target) {
				t.Fatalf("AdmitMove error = %v, want *MoveConflictError", err)
			}
			if target.NodeID != "n2" {
				t.Fatalf("NodeID = %q, want n2", target.NodeID)
			}
			if storedMove(t, kv, "acme") != nil {
				t.Fatal("journal written although a node is not ready")
			}
			if got := poolReserved(t, kv, "bulk"); got != 0 {
				t.Fatalf("bulk reserved = %d, want 0", got)
			}
		})
	}
}

func mustGet(t *testing.T, kv *fakeKV, key string) []byte {
	t.Helper()
	raw, err := kv.Get(context.Background(), key)
	if err != nil || raw == nil {
		t.Fatalf("Get %q: raw=%v err=%v", key, raw, err)
	}
	return raw
}

func TestCatalog_AdmitMove_CapacityExceeded(t *testing.T) {
	cat, kv, _ := newTestCatalog()
	setupMoveCatalog(t, cat, kv, "", "", 100)
	_, err := cat.AdmitMove(context.Background(), admitAcme())
	var target *PoolCapacityExceededError
	if !errors.As(err, &target) {
		t.Fatalf("AdmitMove error = %v, want *PoolCapacityExceededError", err)
	}
	if storedMove(t, kv, "acme") != nil {
		t.Fatal("journal written although capacity was exceeded")
	}
	if got := poolReserved(t, kv, "bulk"); got != 0 {
		t.Fatalf("bulk reserved = %d, want 0", got)
	}
}

func TestCatalog_AdmitMove_BoundedRetries(t *testing.T) {
	cat, kv, tx := newMoveTestCatalog(t)
	tx.forceFailures = maxCASRetries
	_, err := cat.AdmitMove(context.Background(), admitAcme())
	var target *ReservationConflictError
	if !errors.As(err, &target) {
		t.Fatalf("AdmitMove under sustained conflict error = %v, want *ReservationConflictError", err)
	}
	if storedMove(t, kv, "acme") != nil {
		t.Fatal("journal written although every transaction failed")
	}
}

// rewriteRecord decodes the JSON record at key into a value of type T,
// applies edit and stores it back.
func rewriteRecord[T any](t *testing.T, kv *fakeKV, key string, edit func(*T)) {
	t.Helper()
	var v T
	if err := json.Unmarshal(mustGet(t, kv, key), &v); err != nil {
		t.Fatalf("decode %q: %v", key, err)
	}
	edit(&v)
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode %q: %v", key, err)
	}
	if err := kv.Put(context.Background(), key, data); err != nil {
		t.Fatalf("Put %q: %v", key, err)
	}
}

func TestCatalog_AdmitMove_BadRecords(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		spoil func(t *testing.T, kv *fakeKV)
		check func(t *testing.T, err error)
	}{
		{"corrupt assignment", func(t *testing.T, kv *fakeKV) {
			if err := kv.Put(ctx, AssignmentKey("acme"), []byte("{not json")); err != nil {
				t.Fatalf("Put: %v", err)
			}
		}, func(t *testing.T, err error) {
			var target *CorruptRecordError
			if !errors.As(err, &target) || target.Key != AssignmentKey("acme") {
				t.Fatalf("AdmitMove error = %v, want *CorruptRecordError on the assignment", err)
			}
		}},
		{"assignment path escapes", func(t *testing.T, kv *fakeKV) {
			rewriteRecord(t, kv, AssignmentKey("acme"), func(a *Assignment) { a.Path = "../escape" })
		}, func(t *testing.T, err error) {
			var target *InvalidAssignmentPathError
			if !errors.As(err, &target) {
				t.Fatalf("AdmitMove error = %v, want *InvalidAssignmentPathError", err)
			}
		}},
		{"source pool missing", func(t *testing.T, kv *fakeKV) {
			if ok, err := kv.CompareAndDelete(ctx, PoolKey("fast"), mustGet(t, kv, PoolKey("fast"))); err != nil || !ok {
				t.Fatalf("delete fast: ok=%v err=%v", ok, err)
			}
		}, func(t *testing.T, err error) {
			var target *PoolNotFoundError
			if !errors.As(err, &target) {
				t.Fatalf("AdmitMove error = %v, want *PoolNotFoundError", err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat, kv, _ := newMoveTestCatalog(t)
			tc.spoil(t, kv)
			_, err := cat.AdmitMove(ctx, admitAcme())
			tc.check(t, err)
			if storedMove(t, kv, "acme") != nil {
				t.Fatal("journal written over a bad record")
			}
			if got := poolReserved(t, kv, "bulk"); got != 0 {
				t.Fatalf("bulk reserved = %d, want 0", got)
			}
		})
	}
}

func TestCatalog_AdmitMove_StoreFailures(t *testing.T) {
	boom := errors.New("store failed")
	cases := map[string]func(kv *fakeKV, tx *fakeTx, err error){
		"get":     func(kv *fakeKV, _ *fakeTx, err error) { kv.failGet = err },
		"scan":    func(kv *fakeKV, _ *fakeTx, err error) { kv.failScan = err },
		"execute": func(_ *fakeKV, tx *fakeTx, err error) { tx.failExecute = err },
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			cat, kv, tx := newMoveTestCatalog(t)
			set(kv, tx, boom)
			_, err := cat.AdmitMove(context.Background(), admitAcme())
			if !errors.Is(err, boom) {
				t.Fatalf("AdmitMove error = %v, want the store failure", err)
			}
			set(kv, tx, nil)
			if storedMove(t, kv, "acme") != nil {
				t.Fatal("journal written although the store failed")
			}
			if got := poolReserved(t, kv, "bulk"); got != 0 {
				t.Fatalf("bulk reserved = %d, want 0", got)
			}
		})
	}
}

func TestCatalog_AdmitMove_Q9UnregisteredProvider(t *testing.T) {
	cat, kv := newQ9Catalog(t, "dir", "dir")
	rewriteRecord(t, kv, PoolKey("fast"), func(p *Pool) { p.Provider = "retired" })
	_, err := cat.AdmitMove(context.Background(), admitAcme())
	var target *MoveToUnencryptedPoolError
	if !errors.As(err, &target) {
		t.Fatalf("AdmitMove from an unregistered provider error = %v, want *MoveToUnencryptedPoolError", err)
	}
	if storedMove(t, kv, "acme") != nil {
		t.Fatal("journal written for a refused move")
	}
}

func TestCatalog_DeletePool_MoveScanFailure(t *testing.T) {
	cat, kv, _ := newMoveTestCatalog(t)
	ctx := context.Background()
	boom := errors.New("scan failed")
	kv.failScan, kv.failScanPrefix = boom, MoveKeyPrefix
	if err := cat.DeletePool(ctx, "bulk"); !errors.Is(err, boom) {
		t.Fatalf("DeletePool error = %v, want the scan failure", err)
	}
	kv.failScan = nil
	if _, err := cat.GetPool(ctx, "bulk"); err != nil {
		t.Fatalf("GetPool after a failed delete: %v", err)
	}
}

// lockableTestProvider is a Provider that states its volumes are Lockable.
type lockableTestProvider struct{ name string }

func (p *lockableTestProvider) Name() string { return p.name }
func (p *lockableTestProvider) Open(_ context.Context, spec Spec) (Volume, error) {
	return nil, &OpenError{Provider: p.name, Pool: spec.Pool, Err: errors.New("test provider opens nothing")}
}
func (p *lockableTestProvider) OpensLockable() bool { return true }

// silentTestProvider is a Provider that does not implement LockableProvider.
type silentTestProvider struct{ name string }

func (p *silentTestProvider) Name() string { return p.name }
func (p *silentTestProvider) Open(_ context.Context, spec Spec) (Volume, error) {
	return nil, &OpenError{Provider: p.name, Pool: spec.Pool, Err: errors.New("test provider opens nothing")}
}

// moveTestRegistry serves a fixed provider set.
type moveTestRegistry struct{ providers map[string]Provider }

func (r *moveTestRegistry) Register(p Provider) error {
	r.providers[p.Name()] = p
	return nil
}

func (r *moveTestRegistry) Lookup(name string) (Provider, error) {
	p, ok := r.providers[name]
	if !ok {
		return nil, &ProviderNotRegisteredError{Provider: name}
	}
	return p, nil
}

func (r *moveTestRegistry) ListAvailable() []string { return []string{"dir", "luks2", "silent"} }

func newQ9Catalog(t *testing.T, fastProvider, bulkProvider string) (*Catalog, *fakeKV) {
	t.Helper()
	kv := newFakeKV()
	tx := newFakeTx(kv)
	reg := &moveTestRegistry{providers: map[string]Provider{
		"dir":    NewDirProvider(),
		"luks2":  &lockableTestProvider{name: "luks2"},
		"silent": &silentTestProvider{name: "silent"},
	}}
	cat, err := NewCatalog(kv, tx, reg)
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	setupMoveCatalog(t, cat, kv, fastProvider, bulkProvider, 1000)
	return cat, kv
}

func TestCatalog_AdmitMove_Q9LockableToPlainRefused(t *testing.T) {
	cat, kv := newQ9Catalog(t, "luks2", "dir")
	_, err := cat.AdmitMove(context.Background(), admitAcme())
	var target *MoveToUnencryptedPoolError
	if !errors.As(err, &target) {
		t.Fatalf("AdmitMove luks2 to dir error = %v, want *MoveToUnencryptedPoolError", err)
	}
	if target.TenantID != "acme" || target.FromPool != "fast" || target.ToPool != "bulk" {
		t.Fatalf("refusal = %+v, want acme fast to bulk", target)
	}
	// Ruling 1234: the refusal names the operator's remedy.
	if !strings.Contains(err.Error(), "allow_unencrypted_destination") {
		t.Fatalf("refusal %q does not name allow_unencrypted_destination", err.Error())
	}
	if storedMove(t, kv, "acme") != nil {
		t.Fatal("journal written for a refused encrypted-to-unencrypted move")
	}
	if got := poolReserved(t, kv, "bulk"); got != 0 {
		t.Fatalf("bulk reserved = %d, want 0", got)
	}
	assertAssignedTo(t, cat, "fast", 200)
}

// TestCatalog_AdmitMove_Q9AllowedDropIsJournalled: ruling 1234, an admit
// request that carries AllowEncryptionDrop (MoveTenant sets it only after
// the drop was audited) admits a Lockable-to-plain move and journals the
// drop; a plain-to-plain admit journals no drop.
func TestCatalog_AdmitMove_Q9AllowedDropIsJournalled(t *testing.T) {
	ctx := context.Background()
	cat, kv := newQ9Catalog(t, "luks2", "dir")
	req := admitAcme()
	req.AllowEncryptionDrop = true
	req.AuditedFromPool = "fast"
	m, err := cat.AdmitMove(ctx, req)
	if err != nil {
		t.Fatalf("AdmitMove luks2 to dir with AllowEncryptionDrop: %v", err)
	}
	if !m.EncryptionDropped {
		t.Fatalf("admitted move = %+v, want EncryptionDropped", m)
	}
	if stored := storedMove(t, kv, "acme"); stored == nil || !stored.EncryptionDropped {
		t.Fatalf("journal = %+v, want EncryptionDropped", stored)
	}
	got, err := cat.GetMove(ctx, "acme")
	if err != nil || !got.EncryptionDropped {
		t.Fatalf("GetMove = %+v, %v, want EncryptionDropped", got, err)
	}

	plain, plainKV := newQ9Catalog(t, "dir", "dir")
	pm, err := plain.AdmitMove(ctx, req)
	if err != nil {
		t.Fatalf("AdmitMove dir to dir: %v", err)
	}
	if pm.EncryptionDropped {
		t.Fatalf("dir-to-dir move = %+v, want no EncryptionDropped", pm)
	}
	if stored := storedMove(t, plainKV, "acme"); stored == nil || stored.EncryptionDropped {
		t.Fatalf("dir-to-dir journal = %+v, want no EncryptionDropped", stored)
	}
}

func TestCatalog_AdmitMove_Q9PlainToLockable(t *testing.T) {
	cat, _ := newQ9Catalog(t, "dir", "luks2")
	if _, err := cat.AdmitMove(context.Background(), admitAcme()); err != nil {
		t.Fatalf("AdmitMove dir to luks2: %v", err)
	}
}

func TestCatalog_AdmitMove_Q9UnknownSourceFailsClosed(t *testing.T) {
	cat, _ := newQ9Catalog(t, "silent", "dir")
	_, err := cat.AdmitMove(context.Background(), admitAcme())
	var target *MoveToUnencryptedPoolError
	if !errors.As(err, &target) {
		t.Fatalf("AdmitMove from a provider without OpensLockable error = %v, want *MoveToUnencryptedPoolError", err)
	}
}

func TestCatalog_AdmitMove_Q9UnknownDestinationFailsClosed(t *testing.T) {
	cat, kv := newQ9Catalog(t, "luks2", "silent")
	_, err := cat.AdmitMove(context.Background(), admitAcme())
	var target *MoveToUnencryptedPoolError
	if !errors.As(err, &target) {
		t.Fatalf("AdmitMove to a provider without OpensLockable error = %v, want *MoveToUnencryptedPoolError", err)
	}
	if storedMove(t, kv, "acme") != nil {
		t.Fatal("journal written for a refused move")
	}
}

func TestCatalog_Move_Q10RefusalsAfterAdmit(t *testing.T) {
	cat, kv, _ := newMoveTestCatalog(t)
	ctx := context.Background()
	if _, err := cat.AdmitMove(ctx, admitAcme()); err != nil {
		t.Fatalf("AdmitMove: %v", err)
	}
	var conflict *MoveConflictError
	if err := cat.Release(ctx, "acme"); !errors.As(err, &conflict) {
		t.Fatalf("Release error = %v, want *MoveConflictError", err)
	}
	if _, err := cat.AdjustReservation(ctx, "acme", 300); !errors.As(err, &conflict) {
		t.Fatalf("AdjustReservation error = %v, want *MoveConflictError", err)
	}
	assertAssignedTo(t, cat, "fast", 200)
	if got := poolReserved(t, kv, "fast"); got != 200 {
		t.Fatalf("fast reserved = %d, want 200", got)
	}
	if got := poolReserved(t, kv, "bulk"); got != 200 {
		t.Fatalf("bulk reserved = %d, want 200", got)
	}
	for _, pool := range []string{"fast", "bulk"} {
		err := cat.DeletePool(ctx, pool)
		var inUse *PoolInUseError
		if !errors.As(err, &inUse) {
			t.Fatalf("DeletePool(%q) error = %v, want *PoolInUseError", pool, err)
		}
		if !reflect.DeepEqual(inUse.Tenants, []string{"acme"}) {
			t.Fatalf("DeletePool(%q) tenants = %v, want [acme]", pool, inUse.Tenants)
		}
	}
}

// interposeTx runs before once, ahead of the first transaction it carries:
// a racing write lands between a CAS's read and its commit, with no sleeps.
type interposeTx struct {
	*fakeTx
	before func()
}

func (t *interposeTx) Execute(ctx context.Context, compares []lock.Compare, successOps, failureOps []lock.Op) (*lock.TransactionResponse, error) {
	if f := t.before; f != nil {
		t.before = nil
		f()
	}
	return t.fakeTx.Execute(ctx, compares, successOps, failureOps)
}

func TestCatalog_FenceForDelete_ThenAdmitRefused(t *testing.T) {
	cat, kv, _ := newMoveTestCatalog(t)
	ctx := context.Background()
	if err := cat.FenceForDelete(ctx, "acme"); err != nil {
		t.Fatalf("FenceForDelete: %v", err)
	}
	var conflict *MoveConflictError
	if _, err := cat.AdmitMove(ctx, admitAcme()); !errors.As(err, &conflict) || conflict.Reason != "tenant is being deleted" {
		t.Fatalf("AdmitMove after the fence error = %v, want *MoveConflictError tenant is being deleted", err)
	}
	if storedMove(t, kv, "acme") != nil {
		t.Fatal("journal written over a retiring tenant")
	}
	if got := poolReserved(t, kv, "bulk"); got != 0 {
		t.Fatalf("bulk reserved = %d, want 0", got)
	}
	if err := cat.Release(ctx, "acme"); err != nil {
		t.Fatalf("Release of a retiring tenant: %v", err)
	}
	if got := poolReserved(t, kv, "fast"); got != 0 {
		t.Fatalf("fast reserved after release = %d, want 0", got)
	}
}

func TestCatalog_FenceForDelete_AfterAdmitRefused(t *testing.T) {
	cat, kv, _ := newMoveTestCatalog(t)
	ctx := context.Background()
	if _, err := cat.AdmitMove(ctx, admitAcme()); err != nil {
		t.Fatalf("AdmitMove: %v", err)
	}
	var conflict *MoveConflictError
	if err := cat.FenceForDelete(ctx, "acme"); !errors.As(err, &conflict) {
		t.Fatalf("FenceForDelete with a journal error = %v, want *MoveConflictError", err)
	}
	var a Assignment
	if err := json.Unmarshal(mustGet(t, kv, AssignmentKey("acme")), &a); err != nil {
		t.Fatalf("decode assignment: %v", err)
	}
	if a.Retiring {
		t.Fatal("assignment marked retiring although a move runs")
	}
}

func TestCatalog_FenceForDelete_Idempotent(t *testing.T) {
	cat, kv, _ := newMoveTestCatalog(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := cat.FenceForDelete(ctx, "acme"); err != nil {
			t.Fatalf("FenceForDelete #%d: %v", i+1, err)
		}
	}
	var a Assignment
	if err := json.Unmarshal(mustGet(t, kv, AssignmentKey("acme")), &a); err != nil {
		t.Fatalf("decode assignment: %v", err)
	}
	if !a.Retiring || a.Pool != "fast" || a.ReservedBytes != 200 {
		t.Fatalf("assignment = %+v, want retiring on fast with 200 reserved", a)
	}
	if err := cat.FenceForDelete(ctx, "other"); err != nil {
		t.Fatalf("FenceForDelete of an unassigned tenant: %v", err)
	}
}

// A fence that lands between AdmitMove's reads and its commit fails the
// admit's transaction; the retry sees the retiring assignment and refuses.
func TestCatalog_FenceForDelete_RacesAdmit(t *testing.T) {
	cat, kv, tx := newMoveTestCatalog(t)
	ctx := context.Background()
	fenceCat, _, _ := newMoveTestCatalog(t)
	fenceCat.kv, fenceCat.tx = kv, tx
	var fenceErr error
	cat.tx = &interposeTx{fakeTx: tx, before: func() { fenceErr = fenceCat.FenceForDelete(ctx, "acme") }}
	var conflict *MoveConflictError
	if _, err := cat.AdmitMove(ctx, admitAcme()); !errors.As(err, &conflict) || conflict.Reason != "tenant is being deleted" {
		t.Fatalf("AdmitMove racing the fence error = %v, want *MoveConflictError tenant is being deleted", err)
	}
	if fenceErr != nil {
		t.Fatalf("FenceForDelete: %v", fenceErr)
	}
	if storedMove(t, kv, "acme") != nil {
		t.Fatal("journal written over a fenced tenant")
	}
	if got := poolReserved(t, kv, "bulk"); got != 0 {
		t.Fatalf("bulk reserved = %d, want 0", got)
	}
}

func TestCatalog_Move_CorruptJournalFailsClosed(t *testing.T) {
	cat, kv, _ := newMoveTestCatalog(t)
	ctx := context.Background()
	if err := kv.Put(ctx, MoveKey("acme"), []byte("{not json")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	var corrupt *CorruptRecordError
	if err := cat.Release(ctx, "acme"); !errors.As(err, &corrupt) {
		t.Fatalf("Release error = %v, want *CorruptRecordError", err)
	}
	if _, err := cat.AdjustReservation(ctx, "acme", 300); !errors.As(err, &corrupt) {
		t.Fatalf("AdjustReservation error = %v, want *CorruptRecordError", err)
	}
	if err := cat.DeletePool(ctx, "bulk"); !errors.As(err, &corrupt) {
		t.Fatalf("DeletePool error = %v, want *CorruptRecordError", err)
	}
	if corrupt.Key != MoveKey("acme") {
		t.Fatalf("corrupt key = %q, want %q", corrupt.Key, MoveKey("acme"))
	}
	if _, err := cat.AdmitMove(ctx, admitAcme()); !errors.As(err, &corrupt) {
		t.Fatalf("AdmitMove error = %v, want *CorruptRecordError", err)
	}
}

// recordingAdmission is a MoveAdmission double: it records each request and
// answers with nodes or err.
type recordingAdmission struct {
	nodes []string
	err   error
	calls []MoveTenantRequest
}

func (a *recordingAdmission) AdmitTenantMove(_ context.Context, req MoveTenantRequest) ([]string, error) {
	a.calls = append(a.calls, req)
	if a.err != nil {
		return nil, a.err
	}
	return a.nodes, nil
}

// recordingAuditor is a MoveStartAuditor double: it records each begin and
// settle, and fails begin with beginErr.
type recordingAuditor struct {
	beginErr    error
	onBegin     func()
	begins      []MoveTenantRequest
	facts       []MoveStartFacts
	settles     int
	settledMove *Move
	settledErr  error
}

func (a *recordingAuditor) BeginMoveStart(_ context.Context, req MoveTenantRequest, facts MoveStartFacts) (func(*Move, error), error) {
	a.begins = append(a.begins, req)
	a.facts = append(a.facts, facts)
	if a.onBegin != nil {
		a.onBegin()
	}
	if a.beginErr != nil {
		return nil, a.beginErr
	}
	return func(m *Move, err error) {
		a.settles++
		a.settledMove = m
		a.settledErr = err
	}, nil
}

func moveAcme() MoveTenantRequest {
	return MoveTenantRequest{TenantID: "acme", ToPool: "bulk"}
}

func TestCatalog_MoveTenant_NoAdmissionWired(t *testing.T) {
	cat, kv, _ := newMoveTestCatalog(t)
	var target *MoveNotImplementedError
	if _, err := cat.MoveTenant(context.Background(), moveAcme()); !errors.As(err, &target) {
		t.Fatalf("MoveTenant error = %v, want *MoveNotImplementedError", err)
	}
	if storedMove(t, kv, "acme") != nil {
		t.Fatal("journal written with no admission wired")
	}
}

func TestCatalog_MoveTenant_InvalidRequest(t *testing.T) {
	cat, _, _ := newMoveTestCatalog(t)
	adm := &recordingAdmission{nodes: []string{"n1", "n2"}}
	cat.SetMoveAdmission(adm)
	for _, req := range []MoveTenantRequest{
		{ToPool: "bulk"},
		{TenantID: "acme"},
	} {
		var target *InvalidMoveRequestError
		if _, err := cat.MoveTenant(context.Background(), req); !errors.As(err, &target) {
			t.Fatalf("MoveTenant(%+v) error = %v, want *InvalidMoveRequestError", req, err)
		}
	}
	if len(adm.calls) != 0 {
		t.Fatalf("admission called %d times for invalid requests", len(adm.calls))
	}
}

func TestCatalog_MoveTenant_AdmissionRefuses(t *testing.T) {
	cat, kv, _ := newMoveTestCatalog(t)
	refusal := errors.New("tenant not entitled")
	cat.SetMoveAdmission(&recordingAdmission{err: refusal})
	aud := &recordingAuditor{}
	cat.SetMoveStartAuditor(aud)
	if _, err := cat.MoveTenant(context.Background(), moveAcme()); !errors.Is(err, refusal) {
		t.Fatalf("MoveTenant error = %v, want the admission's refusal", err)
	}
	if storedMove(t, kv, "acme") != nil {
		t.Fatal("journal written after the admission refused")
	}
	if len(aud.begins) != 0 {
		t.Fatal("start audited after the admission refused")
	}
}

func TestCatalog_MoveTenant_AuditorRefusesStart(t *testing.T) {
	cat, kv, _ := newMoveTestCatalog(t)
	adm := &recordingAdmission{nodes: []string{"n1", "n2"}}
	cat.SetMoveAdmission(adm)
	auditErr := errors.New("audit sink down")
	cat.SetMoveStartAuditor(&recordingAuditor{beginErr: auditErr})
	if _, err := cat.MoveTenant(context.Background(), moveAcme()); !errors.Is(err, auditErr) {
		t.Fatalf("MoveTenant error = %v, want the auditor's refusal", err)
	}
	if len(adm.calls) != 1 {
		t.Fatalf("admission calls = %d, want 1", len(adm.calls))
	}
	if storedMove(t, kv, "acme") != nil {
		t.Fatal("journal written after the auditor refused the start")
	}
	if got := poolReserved(t, kv, "bulk"); got != 0 {
		t.Fatalf("bulk reserved = %d, want 0", got)
	}
}

func TestCatalog_MoveTenant_Success(t *testing.T) {
	cat, kv, _ := newMoveTestCatalog(t)
	ctx := context.Background()
	adm := &recordingAdmission{nodes: []string{"n1", "n2"}}
	cat.SetMoveAdmission(adm)
	aud := &recordingAuditor{}
	cat.SetMoveStartAuditor(aud)
	m, err := cat.MoveTenant(ctx, moveAcme())
	if err != nil {
		t.Fatalf("MoveTenant: %v", err)
	}
	if !reflect.DeepEqual(adm.calls, []MoveTenantRequest{moveAcme()}) {
		t.Fatalf("admission calls = %+v", adm.calls)
	}
	if m.FromPool != "fast" || m.ToPool != "bulk" || !reflect.DeepEqual(m.NodesPending, []string{"n1", "n2"}) {
		t.Fatalf("move = %+v", m)
	}
	if storedMove(t, kv, "acme") == nil {
		t.Fatal("no journal at MoveKey(acme)")
	}
	if len(aud.begins) != 1 || aud.settles != 1 || aud.settledMove != m || aud.settledErr != nil {
		t.Fatalf("auditor begins=%d settles=%d move=%v err=%v", len(aud.begins), aud.settles, aud.settledMove, aud.settledErr)
	}
	got, err := cat.GetMove(ctx, "acme")
	if err != nil {
		t.Fatalf("GetMove: %v", err)
	}
	if !got.AdmittedAt.Equal(m.AdmittedAt) {
		t.Fatalf("GetMove AdmittedAt %v != %v", got.AdmittedAt, m.AdmittedAt)
	}
	got.AdmittedAt = m.AdmittedAt
	if !reflect.DeepEqual(*got, *m) {
		t.Fatalf("GetMove %+v != MoveTenant %+v", *got, *m)
	}

	// A second move is refused by AdmitMove (Q10) and settle sees it.
	var conflict *MoveConflictError
	if _, err := cat.MoveTenant(ctx, moveAcme()); !errors.As(err, &conflict) {
		t.Fatalf("second MoveTenant error = %v, want *MoveConflictError", err)
	}
	if aud.settles != 2 || aud.settledMove != nil || !errors.As(aud.settledErr, &conflict) {
		t.Fatalf("second settle: settles=%d move=%v err=%v", aud.settles, aud.settledMove, aud.settledErr)
	}
}

func TestCatalog_MoveTenant_NoAuditorWired(t *testing.T) {
	cat, kv, _ := newMoveTestCatalog(t)
	cat.SetMoveAdmission(&recordingAdmission{nodes: []string{"n1", "n2"}})
	if _, err := cat.MoveTenant(context.Background(), moveAcme()); err != nil {
		t.Fatalf("MoveTenant: %v", err)
	}
	if storedMove(t, kv, "acme") == nil {
		t.Fatal("no journal at MoveKey(acme)")
	}
}

// TestCatalog_MoveTenant_Q9WithoutFlagRefused: without the operator's
// allow_unencrypted_destination a Lockable-to-plain move is refused
// (ruling 1234), the auditor's settle sees the refusal and nothing is
// journalled.
func TestCatalog_MoveTenant_Q9WithoutFlagRefused(t *testing.T) {
	cat, kv := newQ9Catalog(t, "luks2", "dir")
	cat.SetMoveAdmission(&recordingAdmission{nodes: []string{"n1", "n2"}})
	aud := &recordingAuditor{}
	cat.SetMoveStartAuditor(aud)
	var refused *MoveToUnencryptedPoolError
	if _, err := cat.MoveTenant(context.Background(), moveAcme()); !errors.As(err, &refused) {
		t.Fatalf("MoveTenant luks2 to dir error = %v, want *MoveToUnencryptedPoolError", err)
	}
	if len(aud.begins) != 1 || aud.settles != 1 || aud.settledMove != nil || !errors.As(aud.settledErr, &refused) {
		t.Fatalf("auditor begins=%d settles=%d move=%v err=%v", len(aud.begins), aud.settles, aud.settledMove, aud.settledErr)
	}
	if aud.facts[0].EncryptionDropped {
		t.Fatalf("facts = %+v, want no EncryptionDropped without the flag", aud.facts[0])
	}
	if storedMove(t, kv, "acme") != nil {
		t.Fatal("journal written for a refused move")
	}
}

// TestCatalog_MoveTenant_Q9FlagAuditsTheDrop: with the flag the auditor is
// told the move drops encryption (and from which pool) before anything is
// admitted, and the admitted move journals the drop.
func TestCatalog_MoveTenant_Q9FlagAuditsTheDrop(t *testing.T) {
	cat, kv := newQ9Catalog(t, "luks2", "dir")
	cat.SetMoveAdmission(&recordingAdmission{nodes: []string{"n1", "n2"}})
	aud := &recordingAuditor{}
	cat.SetMoveStartAuditor(aud)
	req := moveAcme()
	req.AllowUnencryptedDestination = true
	m, err := cat.MoveTenant(context.Background(), req)
	if err != nil {
		t.Fatalf("MoveTenant luks2 to dir with the flag: %v", err)
	}
	if !m.EncryptionDropped {
		t.Fatalf("move = %+v, want EncryptionDropped", m)
	}
	want := MoveStartFacts{FromPool: "fast", EncryptionDropped: true}
	if len(aud.facts) != 1 || aud.facts[0] != want {
		t.Fatalf("auditor facts = %+v, want [%+v]", aud.facts, want)
	}
	if aud.settles != 1 || aud.settledErr != nil || aud.settledMove == nil {
		t.Fatalf("auditor settles=%d move=%v err=%v", aud.settles, aud.settledMove, aud.settledErr)
	}
	if stored := storedMove(t, kv, "acme"); stored == nil || !stored.EncryptionDropped {
		t.Fatalf("journal = %+v, want EncryptionDropped", stored)
	}
}

func TestCatalog_MoveTenant_Q9AuditedSourceChangedIsRefused(t *testing.T) {
	ctx := context.Background()
	cat, kv := newQ9Catalog(t, "luks2", "dir")
	if _, err := cat.CreatePool(ctx, CreatePoolRequest{Name: "vault", Provider: "luks2", CapacityBytes: 1000}); err != nil {
		t.Fatalf("CreatePool vault: %v", err)
	}
	cat.SetMoveAdmission(&recordingAdmission{nodes: []string{"n1", "n2"}})
	aud := &recordingAuditor{onBegin: func() {
		a := storedAssignment(t, kv, "acme")
		a.Pool = "vault"
		data, err := json.Marshal(a)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if err := kv.Put(ctx, AssignmentKey("acme"), data); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}}
	cat.SetMoveStartAuditor(aud)
	destBefore, _, err := cat.getPoolRaw(ctx, "bulk")
	if err != nil {
		t.Fatalf("getPoolRaw bulk: %v", err)
	}
	req := moveAcme()
	req.AllowUnencryptedDestination = true
	_, moveErr := cat.MoveTenant(ctx, req)
	var conflict *MoveConflictError
	if !errors.As(moveErr, &conflict) || !strings.Contains(conflict.Reason, "source pool changed") {
		t.Fatalf("MoveTenant after the audited source changed: err = %v, want *MoveConflictError naming the changed source", moveErr)
	}
	if stored := storedMove(t, kv, "acme"); stored != nil {
		t.Fatalf("journal = %+v, want none", stored)
	}
	destAfter, _, err := cat.getPoolRaw(ctx, "bulk")
	if err != nil {
		t.Fatalf("getPoolRaw bulk: %v", err)
	}
	if destAfter.ReservedBytes != destBefore.ReservedBytes {
		t.Fatalf("destination reserved = %d, want %d", destAfter.ReservedBytes, destBefore.ReservedBytes)
	}
	if aud.settles != 1 || aud.settledErr != moveErr {
		t.Fatalf("auditor settles=%d err=%v, want 1 with %v", aud.settles, aud.settledErr, moveErr)
	}
}

func TestCatalog_AdmitMove_AllowDropWithoutAuditedSourceRefused(t *testing.T) {
	cat, kv := newQ9Catalog(t, "luks2", "dir")
	_, err := cat.AdmitMove(context.Background(), MoveAdmitRequest{
		TenantID:            "acme",
		ToPool:              "bulk",
		ReplicaNodes:        []string{"n1", "n2"},
		AllowEncryptionDrop: true,
	})
	var invalid *InvalidMoveRequestError
	if !errors.As(err, &invalid) {
		t.Fatalf("AdmitMove drop without an audited source: err = %v, want *InvalidMoveRequestError", err)
	}
	if stored := storedMove(t, kv, "acme"); stored != nil {
		t.Fatalf("journal = %+v, want none", stored)
	}
}

// TestCatalog_MoveTenant_Q9DropWithoutAuditorRefused: a drop that no audit
// sink can record never happens.
func TestCatalog_MoveTenant_Q9DropWithoutAuditorRefused(t *testing.T) {
	cat, kv := newQ9Catalog(t, "luks2", "dir")
	cat.SetMoveAdmission(&recordingAdmission{nodes: []string{"n1", "n2"}})
	req := moveAcme()
	req.AllowUnencryptedDestination = true
	var unaudited *MoveEncryptionDropUnauditedError
	if _, err := cat.MoveTenant(context.Background(), req); !errors.As(err, &unaudited) {
		t.Fatalf("MoveTenant error = %v, want *MoveEncryptionDropUnauditedError", err)
	}
	if unaudited.TenantID != "acme" || unaudited.FromPool != "fast" || unaudited.ToPool != "bulk" {
		t.Fatalf("refusal = %+v, want acme fast to bulk", unaudited)
	}
	if storedMove(t, kv, "acme") != nil {
		t.Fatal("journal written for an unaudited encryption drop")
	}
	if got := poolReserved(t, kv, "bulk"); got != 0 {
		t.Fatalf("bulk reserved = %d, want 0", got)
	}
}

// TestCatalog_MoveTenant_Q9FlagOnPlainMoveNoDrop: the flag on a move that
// drops nothing admits it as usual and tells the auditor no drop.
func TestCatalog_MoveTenant_Q9FlagOnPlainMoveNoDrop(t *testing.T) {
	cat, kv := newQ9Catalog(t, "dir", "dir")
	cat.SetMoveAdmission(&recordingAdmission{nodes: []string{"n1", "n2"}})
	aud := &recordingAuditor{}
	cat.SetMoveStartAuditor(aud)
	req := moveAcme()
	req.AllowUnencryptedDestination = true
	m, err := cat.MoveTenant(context.Background(), req)
	if err != nil {
		t.Fatalf("MoveTenant dir to dir with the flag: %v", err)
	}
	if m.EncryptionDropped {
		t.Fatalf("move = %+v, want no EncryptionDropped", m)
	}
	if len(aud.facts) != 1 || aud.facts[0].EncryptionDropped || aud.facts[0].FromPool != "fast" {
		t.Fatalf("auditor facts = %+v, want fast, no drop", aud.facts)
	}
	if stored := storedMove(t, kv, "acme"); stored == nil || stored.EncryptionDropped {
		t.Fatalf("journal = %+v, want no EncryptionDropped", stored)
	}
}

func TestCatalog_GetMove(t *testing.T) {
	cat, kv, _ := newMoveTestCatalog(t)
	ctx := context.Background()
	var invalid *InvalidMoveRequestError
	if _, err := cat.GetMove(ctx, ""); !errors.As(err, &invalid) {
		t.Fatalf("GetMove(\"\") error = %v, want *InvalidMoveRequestError", err)
	}
	var notFound *MoveNotFoundError
	if _, err := cat.GetMove(ctx, "acme"); !errors.As(err, &notFound) || notFound.TenantID != "acme" {
		t.Fatalf("GetMove error = %v, want *MoveNotFoundError for acme", err)
	}
	if err := kv.Put(ctx, MoveKey("acme"), []byte("{not json")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	var corrupt *CorruptRecordError
	if _, err := cat.GetMove(ctx, "acme"); !errors.As(err, &corrupt) {
		t.Fatalf("GetMove error = %v, want *CorruptRecordError", err)
	}
}

func TestCatalog_SetMoveAdmissionNilUnwires(t *testing.T) {
	cat, _, _ := newMoveTestCatalog(t)
	adm := &recordingAdmission{nodes: []string{"n1", "n2"}}
	cat.SetMoveAdmission(adm)
	cat.SetMoveStartAuditor(nil)
	cat.SetMoveAdmission(nil)
	var target *MoveNotImplementedError
	if _, err := cat.MoveTenant(context.Background(), moveAcme()); !errors.As(err, &target) {
		t.Fatalf("MoveTenant error = %v, want *MoveNotImplementedError", err)
	}
	if len(adm.calls) != 0 {
		t.Fatal("unwired admission was called")
	}
}

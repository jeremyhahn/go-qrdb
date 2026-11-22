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
	"testing"
	"time"
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
	if m.ReservedBytes != 200 || m.AllowUnencryptedDestination || m.AdmittedAt.IsZero() {
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

func TestCatalog_AdmitMove_Q9LockableToPlain(t *testing.T) {
	cat, kv := newQ9Catalog(t, "luks2", "dir")
	_, err := cat.AdmitMove(context.Background(), admitAcme())
	var target *MoveConflictError
	if !errors.As(err, &target) {
		t.Fatalf("AdmitMove without the flag error = %v, want *MoveConflictError", err)
	}
	if storedMove(t, kv, "acme") != nil {
		t.Fatal("journal written without the flag")
	}
	req := admitAcme()
	req.AllowUnencryptedDestination = true
	m, err := cat.AdmitMove(context.Background(), req)
	if err != nil {
		t.Fatalf("AdmitMove with the flag: %v", err)
	}
	if !m.AllowUnencryptedDestination {
		t.Fatal("returned move lost the flag")
	}
	if stored := storedMove(t, kv, "acme"); stored == nil || !stored.AllowUnencryptedDestination {
		t.Fatalf("journal = %+v, want the flag set", stored)
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
	var target *MoveConflictError
	if !errors.As(err, &target) {
		t.Fatalf("AdmitMove from a provider without OpensLockable error = %v, want *MoveConflictError", err)
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

func TestCatalog_MoveTenantGetMove_StillNotImplemented(t *testing.T) {
	cat, _, _ := newMoveTestCatalog(t)
	ctx := context.Background()
	var target *MoveNotImplementedError
	if _, err := cat.MoveTenant(ctx, "acme", "bulk"); !errors.As(err, &target) {
		t.Fatalf("MoveTenant error = %v, want *MoveNotImplementedError", err)
	}
	if _, err := cat.GetMove(ctx, "acme"); !errors.As(err, &target) {
		t.Fatalf("GetMove error = %v, want *MoveNotImplementedError", err)
	}
}

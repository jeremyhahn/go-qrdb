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
	begins      []MoveTenantRequest
	settles     int
	settledMove *Move
	settledErr  error
}

func (a *recordingAuditor) BeginMoveStart(_ context.Context, req MoveTenantRequest) (func(*Move, error), error) {
	a.begins = append(a.begins, req)
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

func TestCatalog_MoveTenant_Q9FlagReachesTheJournal(t *testing.T) {
	cat, kv := newQ9Catalog(t, "luks2", "dir")
	cat.SetMoveAdmission(&recordingAdmission{nodes: []string{"n1", "n2"}})
	aud := &recordingAuditor{}
	cat.SetMoveStartAuditor(aud)
	var conflict *MoveConflictError
	if _, err := cat.MoveTenant(context.Background(), moveAcme()); !errors.As(err, &conflict) {
		t.Fatalf("MoveTenant without the flag error = %v, want *MoveConflictError", err)
	}
	req := moveAcme()
	req.AllowUnencryptedDestination = true
	if _, err := cat.MoveTenant(context.Background(), req); err != nil {
		t.Fatalf("MoveTenant with the flag: %v", err)
	}
	if len(aud.begins) != 2 || aud.begins[0].AllowUnencryptedDestination || !aud.begins[1].AllowUnencryptedDestination {
		t.Fatalf("audited begins = %+v, want the flag off then on", aud.begins)
	}
	if stored := storedMove(t, kv, "acme"); stored == nil || !stored.AllowUnencryptedDestination {
		t.Fatalf("journal = %+v, want the flag set", stored)
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

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
	"testing"
	"time"
)

func newTestReporter(t *testing.T, nodeID string, opts ...ReporterOption) (*Reporter, *fakeMVCC, *fakeLeases) {
	t.Helper()
	leases := newFakeLeases()
	mvcc := newFakeMVCC(leases)
	r, err := NewReporter(mvcc, leases, nodeID, opts...)
	if err != nil {
		t.Fatalf("NewReporter: %v", err)
	}
	return r, mvcc, leases
}

func TestNewReporter_RequiresDependencies(t *testing.T) {
	leases := newFakeLeases()
	mvcc := newFakeMVCC(leases)

	if _, err := NewReporter(nil, leases, "node-1"); err == nil {
		t.Fatal("NewReporter(nil mvcc) should fail")
	}
	if _, err := NewReporter(mvcc, nil, "node-1"); err == nil {
		t.Fatal("NewReporter(nil leases) should fail")
	}
	if _, err := NewReporter(mvcc, leases, ""); err == nil {
		t.Fatal("NewReporter(empty node id) should fail")
	}
}

func TestReporter_ReportWritesLeaseAttachedRecord(t *testing.T) {
	r, mvcc, leases := newTestReporter(t, "node-1")
	ctx := context.Background()

	report := NodeReport{Pools: map[string]PoolOnNode{
		"fast": {Mapped: true, Health: Health{State: StateReady}},
	}}
	if err := r.Report(ctx, report); err != nil {
		t.Fatalf("Report: %v", err)
	}

	raw, err := mvcc.Get(ctx, NodeReportKey("node-1"))
	if err != nil || raw == nil {
		t.Fatalf("Get after Report: raw=%v err=%v", raw, err)
	}
	var got NodeReport
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.NodeID != "node-1" {
		t.Fatalf("Report did not stamp NodeID, got %+v", got)
	}
	if got.ReportedAt.IsZero() {
		t.Fatal("Report did not stamp ReportedAt")
	}
	if len(leases.keys) != 1 {
		t.Fatalf("leases.keys = %v, want exactly one lease covering the record", leases.keys)
	}
}

func TestReporter_ReportRejectsForeignNodeID(t *testing.T) {
	r, _, _ := newTestReporter(t, "node-1")
	err := r.Report(context.Background(), NodeReport{NodeID: "node-2"})
	if err == nil {
		t.Fatal("Report(foreign node id) should fail")
	}
}

func TestReporter_SecondReportReusesTheLease(t *testing.T) {
	r, _, leases := newTestReporter(t, "node-1")
	ctx := context.Background()

	if err := r.Report(ctx, NodeReport{}); err != nil {
		t.Fatalf("Report (first): %v", err)
	}
	firstLease := r.leaseID
	if err := r.Report(ctx, NodeReport{}); err != nil {
		t.Fatalf("Report (second): %v", err)
	}
	if r.leaseID != firstLease {
		t.Fatalf("second Report granted a new lease (%d != %d); want KeepAlive on the same one", r.leaseID, firstLease)
	}
	if _, err := leases.KeepAlive(ctx, firstLease); err != nil {
		t.Fatalf("the lease from the first Report should still be alive: %v", err)
	}
}

func TestReporter_SelfHealsAfterLeaseLost(t *testing.T) {
	r, _, leases := newTestReporter(t, "node-1")
	ctx := context.Background()

	if err := r.Report(ctx, NodeReport{}); err != nil {
		t.Fatalf("Report (first): %v", err)
	}
	lost := r.leaseID
	leases.expire(lost) // simulate the engine sweeping the lease away

	if err := r.Report(ctx, NodeReport{}); err != nil {
		t.Fatalf("Report after lease loss should self-heal, got: %v", err)
	}
	if r.leaseID == lost {
		t.Fatal("Report after lease loss should have granted a fresh lease")
	}
}

func TestReporter_ReportPropagatesGrantFailure(t *testing.T) {
	r, _, leases := newTestReporter(t, "node-1")
	wantErr := errors.New("grant unavailable")
	leases.setFailGrant(wantErr)
	if err := r.Report(context.Background(), NodeReport{}); !errors.Is(err, wantErr) {
		t.Fatalf("Report error = %v, want %v", err, wantErr)
	}
}

func TestReporter_StartPublishesThenRefreshes(t *testing.T) {
	r, mvcc, _ := newTestReporter(t, "node-1", WithReporterLeaseTTL(50*time.Millisecond))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	calls := 0
	build := func() NodeReport {
		calls++
		return NodeReport{Pools: map[string]PoolOnNode{"fast": {Mapped: true}}}
	}
	if err := r.Start(ctx, build); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = r.Close() }()

	if calls != 1 {
		t.Fatalf("Start should publish once synchronously before returning, calls=%d", calls)
	}
	if raw, err := mvcc.Get(context.Background(), NodeReportKey("node-1")); err != nil || raw == nil {
		t.Fatalf("Start did not publish an initial report: raw=%v err=%v", raw, err)
	}
}

func TestReporter_StartNotIdempotent(t *testing.T) {
	r, _, _ := newTestReporter(t, "node-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	build := func() NodeReport { return NodeReport{} }
	if err := r.Start(ctx, build); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = r.Close() }()

	if err := r.Start(ctx, build); err == nil {
		t.Fatal("a second Start should fail")
	}
}

func TestReporter_CloseRevokesAndDeletesTheRecord(t *testing.T) {
	r, mvcc, _ := newTestReporter(t, "node-1")
	ctx := context.Background()
	if err := r.Report(ctx, NodeReport{}); err != nil {
		t.Fatalf("Report: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	raw, err := mvcc.Get(ctx, NodeReportKey("node-1"))
	if err != nil {
		t.Fatalf("Get after Close: %v", err)
	}
	if raw != nil {
		t.Fatal("Close should revoke the lease and delete the published report")
	}
	// Idempotent.
	if err := r.Close(); err != nil {
		t.Fatalf("Close (second): %v", err)
	}
}

func TestReporter_CloseBeforeAnyReportIsANoOp(t *testing.T) {
	r, _, _ := newTestReporter(t, "node-1")
	if err := r.Close(); err != nil {
		t.Fatalf("Close (never reported): %v", err)
	}
}

func TestReporter_WithReporterLeaseTTL(t *testing.T) {
	leases := newFakeLeases()
	mvcc := newFakeMVCC(leases)
	r, err := NewReporter(mvcc, leases, "node-1", WithReporterLeaseTTL(30*time.Second))
	if err != nil {
		t.Fatalf("NewReporter: %v", err)
	}
	if r.ttl != 30*time.Second {
		t.Fatalf("ttl = %v, want 30s", r.ttl)
	}

	r2, err := NewReporter(mvcc, leases, "node-2", WithReporterLeaseTTL(-1))
	if err != nil {
		t.Fatalf("NewReporter: %v", err)
	}
	if r2.ttl != DefaultLeaseTTL {
		t.Fatalf("ttl after WithReporterLeaseTTL(-1) = %v, want default %v", r2.ttl, DefaultLeaseTTL)
	}
}

func TestReporter_WithReporterInterval(t *testing.T) {
	r, _, _ := newTestReporter(t, "node-1", WithReporterInterval(time.Second))
	if r.interval != time.Second {
		t.Fatalf("interval = %v, want 1s", r.interval)
	}

	r2, _, _ := newTestReporter(t, "node-2", WithReporterInterval(0))
	if r2.interval != DefaultReportInterval {
		t.Fatalf("interval after WithReporterInterval(0) = %v, want default %v", r2.interval, DefaultReportInterval)
	}
}

// TestReporter_StartRetriesAFailedInitialReport pins the boot-time case: the
// first publish fails (no system-shard leader yet), Start still succeeds, and
// the refresh loop publishes the report once the failure clears. A Reporter
// that gave up here left its node out of every raft placement.
func TestReporter_StartRetriesAFailedInitialReport(t *testing.T) {
	r, mvcc, leases := newTestReporter(t, "node-1", WithReporterInterval(10*time.Millisecond))
	leases.setFailGrant(errors.New("not leader for shard 1, leader unknown"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	build := func() NodeReport {
		return NodeReport{Pools: map[string]PoolOnNode{"fast": {Mapped: true}}}
	}
	if err := r.Start(ctx, build); err != nil {
		t.Fatalf("Start with a failing initial report = %v, want nil", err)
	}
	defer func() { _ = r.Close() }()

	if raw, err := mvcc.Get(context.Background(), NodeReportKey("node-1")); err != nil || raw != nil {
		t.Fatalf("report published while Grant fails: raw=%v err=%v", raw, err)
	}

	leases.setFailGrant(nil)
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, err := mvcc.Get(context.Background(), NodeReportKey("node-1"))
		if err == nil && raw != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("refresh loop never published the report after the failure cleared: raw=%v err=%v", raw, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestReporter_WithReporterLogger(t *testing.T) {
	leases := newFakeLeases()
	mvcc := newFakeMVCC(leases)
	r, err := NewReporter(mvcc, leases, "node-1", WithReporterLogger(nil))
	if err != nil {
		t.Fatalf("NewReporter: %v", err)
	}
	if r.logger == nil {
		t.Fatal("WithReporterLogger(nil) left the logger nil")
	}
}

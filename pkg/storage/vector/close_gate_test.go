// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package vector

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch/syncwatchtest"
)

// holdingClock parks the first armed call to Now until release is closed.
// durably reads the clock inside the close gate and before the DB write, so
// a parked Now is a write held inside the storage while Close runs.
type holdingClock struct {
	*clocktest.Fake
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func newHoldingClock() *holdingClock {
	return &holdingClock{
		Fake:    clocktest.New(),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (c *holdingClock) Now() time.Time {
	c.once.Do(func() {
		close(c.entered)
		<-c.release
	})
	return c.Fake.Now()
}

// gatedVectorStore is an in-memory store that syncs its writes, admits one
// read and one write at a time, and times its syncs on hold.
func gatedVectorStore(t *testing.T, hold *holdingClock) *PebbleVectorStorage {
	t.Helper()
	syncwatchtest.FreshRegistry(t)
	cfg := DefaultConfig("/vec")
	cfg.SyncWrites = true
	cfg.MaxConcurrentReads = 1
	cfg.MaxConcurrentWrites = 1
	s, err := NewPebbleVectorStorage(cfg, &testLogger{}, inMemory)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.syncs = syncwatch.ForStorage(syncwatch.BackendPebble, syncwatch.WithClock(hold))
	return s
}

func gateTestVector(id uint64) *VectorValue {
	return &VectorValue{ID: id, Version: 1, Embedding: []float32{1, 2, 3}, Metadata: map[string]string{"k": "v"}}
}

// waitGateHeld returns once some operation holds the close gate shared.
func waitGateHeld(s *PebbleVectorStorage) {
	for s.gate.TryLock() {
		s.gate.Unlock()
		runtime.Gosched()
	}
}

// waitCloseQueued returns once a Close is waiting for the gate: a pending
// writer makes TryRLock fail even while readers still hold it.
func waitCloseQueued(s *PebbleVectorStorage) {
	for s.gate.TryRLock() {
		s.gate.RUnlock()
		runtime.Gosched()
	}
}

func closeAsync(s *PebbleVectorStorage) <-chan error {
	done := make(chan error, 1)
	go func() { done <- s.Close() }()
	return done
}

func TestPebbleVectorClose_WaitsForHeldWrite(t *testing.T) {
	hold := newHoldingClock()
	s := gatedVectorStore(t, hold)
	ctx := context.Background()

	upserted := make(chan error, 1)
	go func() { upserted <- s.UpsertVector(ctx, "ns", gateTestVector(1)) }()
	<-hold.entered

	closed := closeAsync(s)
	waitCloseQueued(s)
	require.False(t, s.closed.Load(), "Close marked the storage closed under a held write")

	close(hold.release)
	require.NoError(t, <-upserted, "the held write must complete against a live DB")
	require.NoError(t, <-closed)

	_, err := s.GetVector(ctx, "ns", 1)
	require.ErrorIs(t, err, ErrStorageClosed)
}

func TestPebbleVectorClose_WaitsForHeldRead(t *testing.T) {
	hold := newHoldingClock()
	close(hold.release)
	s := gatedVectorStore(t, hold)
	ctx := context.Background()
	require.NoError(t, s.UpsertVector(ctx, "ns", gateTestVector(7)))

	// Take the one read slot, so GetVector parks inside the gate.
	s.readSem <- struct{}{}
	type result struct {
		vec *VectorValue
		err error
	}
	got := make(chan result, 1)
	go func() {
		vec, err := s.GetVector(ctx, "ns", 7)
		got <- result{vec, err}
	}()
	waitGateHeld(s)

	closed := closeAsync(s)
	waitCloseQueued(s)
	require.False(t, s.closed.Load(), "Close marked the storage closed under a held read")

	<-s.readSem
	r := <-got
	require.NoError(t, r.err, "the held read must complete against a live DB")
	require.Equal(t, uint64(7), r.vec.ID)
	require.NoError(t, <-closed)

	_, err := s.ScanNamespace(ctx, "ns", nil)
	require.ErrorIs(t, err, ErrStorageClosed)
}

func TestPebbleVectorClose_RefusesEveryOperationAfterClose(t *testing.T) {
	hold := newHoldingClock()
	close(hold.release)
	s := gatedVectorStore(t, hold)
	ctx := context.Background()
	require.NoError(t, s.CreateNamespace(ctx, &NamespaceInfo{Name: "ns", Dimension: 3}))
	require.NoError(t, s.Close())

	ops := map[string]func() error{
		"UpsertVector":       func() error { return s.UpsertVector(ctx, "ns", gateTestVector(1)) },
		"GetVector":          func() error { _, err := s.GetVector(ctx, "ns", 1); return err },
		"DeleteVector":       func() error { return s.DeleteVector(ctx, "ns", 1) },
		"BatchUpsertVectors": func() error { return s.BatchUpsertVectors(ctx, "ns", []*VectorValue{gateTestVector(1)}) },
		"BatchDeleteVectors": func() error { return s.BatchDeleteVectors(ctx, "ns", []uint64{1}) },
		"ScanByMetadata":     func() error { _, err := s.ScanByMetadata(ctx, "ns", "k", "v", nil); return err },
		"ScanByMultipleFilters": func() error {
			_, err := s.ScanByMultipleFilters(ctx, "ns", []MetadataFilter{{Field: "k", Value: "v"}}, nil)
			return err
		},
		"ScanNamespace":    func() error { _, err := s.ScanNamespace(ctx, "ns", nil); return err },
		"CreateNamespace":  func() error { return s.CreateNamespace(ctx, &NamespaceInfo{Name: "other", Dimension: 3}) },
		"DeleteNamespace":  func() error { return s.DeleteNamespace(ctx, "ns") },
		"ListNamespaces":   func() error { _, err := s.ListNamespaces(ctx); return err },
		"GetNamespaceInfo": func() error { _, err := s.GetNamespaceInfo(ctx, "ns"); return err },
		"GetStats":         func() error { _, err := s.GetStats(ctx, "ns"); return err },
		"NextID":           func() error { _, err := s.NextID("ns"); return err },
		"BatchAllocateIDs": func() error { _, err := s.BatchAllocateIDs("ns", 2); return err },
		"Compact":          func() error { return s.Compact(ctx, "ns") },
		"CleanupTombstones": func() error {
			return s.CleanupTombstones(ctx, time.Hour)
		},
	}
	for name, op := range ops {
		require.ErrorIs(t, op(), ErrStorageClosed, name)
	}
	require.True(t, s.gate.TryLock(), "a refused operation must not keep the gate")
	s.gate.Unlock()
}

func TestPebbleVectorClose_ConcurrentClosesCloseTheDBOnce(t *testing.T) {
	hold := newHoldingClock()
	s := gatedVectorStore(t, hold)

	upserted := make(chan error, 1)
	go func() { upserted <- s.UpsertVector(context.Background(), "ns", gateTestVector(1)) }()
	<-hold.entered

	// Every Close queues behind the held write; a second db.Close on a
	// closed pebble.DB panics, so all of them returning nil is the proof.
	const closers = 8
	results := make([]<-chan error, closers)
	for i := range results {
		results[i] = closeAsync(s)
	}
	waitCloseQueued(s)
	close(hold.release)

	require.NoError(t, <-upserted)
	for _, r := range results {
		require.NoError(t, <-r)
	}
}

func TestPebbleVectorClose_SecondCloseIsNoOp(t *testing.T) {
	hold := newHoldingClock()
	close(hold.release)
	s := gatedVectorStore(t, hold)
	require.NoError(t, s.Close())
	require.NoError(t, s.Close())
	require.True(t, s.closed.Load())
}

func TestPebbleVectorEnter_AdmitsUntilClosed(t *testing.T) {
	hold := newHoldingClock()
	close(hold.release)
	s := gatedVectorStore(t, hold)

	require.True(t, s.enter())
	require.False(t, s.gate.TryLock(), "an admitted operation holds the gate")
	s.leave()

	require.NoError(t, s.Close())
	require.False(t, s.enter())
	require.True(t, s.gate.TryLock(), "a refused enter holds nothing")
	s.gate.Unlock()
}

func TestPebbleVectorLeave_LetsCloseProceed(t *testing.T) {
	hold := newHoldingClock()
	close(hold.release)
	s := gatedVectorStore(t, hold)

	require.True(t, s.enter())
	closed := closeAsync(s)
	waitCloseQueued(s)
	require.False(t, s.closed.Load())

	s.leave()
	require.NoError(t, <-closed)
	require.True(t, s.closed.Load())
}

func TestPebbleVectorInnerBodies_RunInsideTheGate(t *testing.T) {
	hold := newHoldingClock()
	close(hold.release)
	s := gatedVectorStore(t, hold)
	ctx := context.Background()
	require.NoError(t, s.UpsertVector(ctx, "ns", gateTestVector(3)))

	require.True(t, s.enter())
	all, err := s.scanNamespace("ns", nil)
	require.NoError(t, err)
	require.Len(t, all, 1)
	byMeta, err := s.scanByMetadata("ns", "k", "v", nil)
	require.NoError(t, err)
	require.Len(t, byMeta, 1)
	stats, err := s.getStats("ns")
	require.NoError(t, err)
	require.NotZero(t, stats.Count)
	s.leave()
}

func TestPebbleVectorNestedReads_DoNotRetakeTheGate(t *testing.T) {
	hold := newHoldingClock()
	close(hold.release)
	s := gatedVectorStore(t, hold)
	ctx := context.Background()
	require.NoError(t, s.CreateNamespace(ctx, &NamespaceInfo{Name: "ns", Dimension: 3}))
	require.NoError(t, s.UpsertVector(ctx, "ns", gateTestVector(4)))

	// With a Close queued, a second shared acquisition would block forever;
	// the outer hold here stands in for the method's own enter.
	require.True(t, s.enter())
	closed := closeAsync(s)
	waitCloseQueued(s)

	all, err := s.scanNamespace("ns", nil)
	require.NoError(t, err)
	require.Len(t, all, 1)
	filtered, err := s.scanByMetadata("ns", "k", "v", nil)
	require.NoError(t, err)
	require.Len(t, filtered, 1)
	_, err = s.getStats("ns")
	require.NoError(t, err)
	s.leave()
	require.NoError(t, <-closed)
}

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
	"log/slog"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch/syncwatchtest"
)

const syncFamily = "qrdb_storage_sync_duration_seconds"

// steppingClock is a fake clock that moves forward by step every time Now is
// read, so a sync timed between two reads takes exactly step.
type steppingClock struct {
	*clocktest.Fake
	step time.Duration
}

func (c *steppingClock) Now() time.Time {
	now := c.Fake.Now()
	c.Advance(c.step)
	return now
}

// inMemory opens the store on an in-memory filesystem.
func inMemory(o *pebble.Options) { o.FS = vfs.NewMem() }

// syncedVectorStore opens an in-memory vector store with the given
// SyncWrites, timing its syncs through an observer whose every sync takes
// step, logging to log, and recording into a fresh process registry it
// returns.
func syncedVectorStore(t *testing.T, syncWrites bool, step time.Duration, log *syncwatchtest.Log) (*PebbleVectorStorage, *prometheus.Registry) {
	t.Helper()
	reg := syncwatchtest.FreshRegistry(t)
	cfg := DefaultConfig("/vec")
	cfg.SyncWrites = syncWrites
	s, err := NewPebbleVectorStorage(cfg, &testLogger{}, inMemory)
	if err != nil {
		t.Fatalf("NewPebbleVectorStorage: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.syncs = syncwatch.ForStorage(syncwatch.BackendPebble,
		syncwatch.WithClock(&steppingClock{Fake: clocktest.New(), step: step}),
		syncwatch.WithLogger(log.Logger()),
		syncwatch.WithThreshold(100*time.Millisecond),
		syncwatch.WithAttrs(slog.String("dir", "/vec")),
	)
	return s, reg
}

// writeEveryPath drives each write the store commits on its own: namespace
// creation (metadata and ID sequence), an upsert, a delete, a tombstone
// cleanup and the namespace's deletion.
func writeEveryPath(t *testing.T, s *PebbleVectorStorage) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateNamespace(ctx, &NamespaceInfo{Name: "docs", Dimension: 4}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	vec := &VectorValue{ID: 1, Version: 1, Embedding: []float32{1, 2, 3, 4}}
	if err := s.UpsertVector(ctx, "docs", vec); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	if err := s.DeleteVector(ctx, "docs", 1); err != nil {
		t.Fatalf("DeleteVector: %v", err)
	}
	batched := &VectorValue{ID: 2, Version: 1, Embedding: []float32{4, 3, 2, 1}}
	if err := s.BatchUpsertVectors(ctx, "docs", []*VectorValue{batched}); err != nil {
		t.Fatalf("BatchUpsertVectors: %v", err)
	}
	if err := s.BatchDeleteVectors(ctx, "docs", []uint64{2}); err != nil {
		t.Fatalf("BatchDeleteVectors: %v", err)
	}
	// A negative age puts the cutoff in the future, so the tombstone just
	// written is old enough to collect without waiting.
	if err := s.CleanupTombstones(ctx, -time.Hour); err != nil {
		t.Fatalf("CleanupTombstones: %v", err)
	}
	if err := s.DeleteNamespace(ctx, "docs"); err != nil {
		t.Fatalf("DeleteNamespace: %v", err)
	}
}

func TestVectorSyncedWrites_EachSyncIsObserved(t *testing.T) {
	s, reg := syncedVectorStore(t, true, time.Millisecond, syncwatchtest.NewLog())

	writeEveryPath(t, s)

	count, sum := syncwatchtest.HistogramSample(t, reg, syncFamily, "backend", syncwatch.BackendPebble)
	if count != 7 {
		t.Fatalf("sync count = %d, want 7 (namespace create, upsert, delete, batch upsert, batch delete, tombstone cleanup, namespace delete)", count)
	}
	if want := (7 * time.Millisecond).Seconds(); sum != want {
		t.Fatalf("sync sum = %v, want %v", sum, want)
	}
}

func TestVectorUnsyncedWrites_AreNotObserved(t *testing.T) {
	s, reg := syncedVectorStore(t, false, time.Millisecond, syncwatchtest.NewLog())

	writeEveryPath(t, s)

	if syncwatchtest.HasFamily(t, reg, syncFamily) {
		if count, _ := syncwatchtest.HistogramSample(t, reg, syncFamily, "backend", syncwatch.BackendPebble); count != 0 {
			t.Fatalf("sync count = %d for NoSync writes, want 0", count)
		}
	}
}

// TestVectorSyncedBatchCommit_SyncsOnce: on a store that syncs writes, each
// batch commit is one timed pebble.Sync write, and a slow one warns once.
func TestVectorSyncedBatchCommit_SyncsOnce(t *testing.T) {
	log := syncwatchtest.NewLog()
	s, reg := syncedVectorStore(t, true, 250*time.Millisecond, log)
	ctx := context.Background()
	vecs := []*VectorValue{
		{ID: 1, Version: 1, Embedding: []float32{1, 2, 3, 4}},
		{ID: 2, Version: 1, Embedding: []float32{4, 3, 2, 1}},
	}

	if err := s.BatchUpsertVectors(ctx, "docs", vecs); err != nil {
		t.Fatalf("BatchUpsertVectors: %v", err)
	}

	count, _ := syncwatchtest.HistogramSample(t, reg, syncFamily, "backend", syncwatch.BackendPebble)
	if count != 1 {
		t.Fatalf("sync count = %d for one batch commit of two vectors, want 1", count)
	}
	recs := log.Records()
	if len(recs) != 1 || recs[0].Attrs["took"] != 250*time.Millisecond ||
		recs[0].Attrs["backend"] != syncwatch.BackendPebble {
		t.Fatalf("warnings = %+v, want one slow pebble sync of 250ms", recs)
	}
}

// TestVectorBatch_WithoutStoreCommitsNoSync: a batch built without a store
// commits through noSync, which writes with pebble.NoSync.
func TestVectorBatch_WithoutStoreCommitsNoSync(t *testing.T) {
	var got *pebble.WriteOptions
	if err := noSync(func(o *pebble.WriteOptions) error { got = o; return nil }); err != nil {
		t.Fatalf("noSync: %v", err)
	}
	if got != pebble.NoSync {
		t.Fatalf("noSync wrote with %+v, want pebble.NoSync", got)
	}

	s, _ := syncedVectorStore(t, false, time.Millisecond, syncwatchtest.NewLog())
	b := newVectorBatch(s.db, s.metrics)
	defer func() { _ = b.Close() }()
	if err := b.UpsertVector("docs", &VectorValue{ID: 1, Version: 1, Embedding: []float32{1, 2, 3, 4}}); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

func TestVectorSlowSync_WarnsOnceWithBackendAndDir(t *testing.T) {
	log := syncwatchtest.NewLog()
	s, _ := syncedVectorStore(t, true, 250*time.Millisecond, log)

	if err := s.CreateNamespace(context.Background(), &NamespaceInfo{Name: "docs", Dimension: 4}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}

	recs := log.Records()
	if len(recs) != 1 {
		t.Fatalf("got %d warnings, want 1 (CreateNamespace is one synced commit)", len(recs))
	}
	r := recs[0]
	if r.Level != slog.LevelWarn || r.Attrs["took"] != 250*time.Millisecond ||
		r.Attrs["threshold"] != 100*time.Millisecond ||
		r.Attrs["backend"] != syncwatch.BackendPebble || r.Attrs["dir"] != "/vec" {
		t.Fatalf("warning = %+v", r)
	}
}

func TestVectorFastSync_DoesNotWarn(t *testing.T) {
	log := syncwatchtest.NewLog()
	s, _ := syncedVectorStore(t, true, 5*time.Millisecond, log)

	writeEveryPath(t, s)

	if n := len(log.Records()); n != 0 {
		t.Fatalf("got %d warnings for 5ms syncs, want 0", n)
	}
}

func TestNewPebbleVectorStorage_HasDefaultSyncObserver(t *testing.T) {
	s, err := NewPebbleVectorStorage(DefaultConfig("/vec"), &testLogger{}, inMemory)
	if err != nil {
		t.Fatalf("NewPebbleVectorStorage: %v", err)
	}
	defer s.Close()
	if s.syncs == nil {
		t.Fatal("NewPebbleVectorStorage built no sync observer")
	}
}

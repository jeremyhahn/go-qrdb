// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package pebble

import (
	"context"
	"log/slog"
	"testing"
	"time"

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

// syncedStore opens an in-memory store with the given SyncWrites, timing its
// syncs through an observer whose every sync takes step, logging to log, and
// recording into a fresh process registry it returns.
func syncedStore(t *testing.T, syncWrites bool, step time.Duration, log *syncwatchtest.Log) (*PebbleStorage, *prometheus.Registry) {
	t.Helper()
	reg := syncwatchtest.FreshRegistry(t)
	obs := syncwatch.ForStorage(syncwatch.BackendPebble,
		syncwatch.WithClock(&steppingClock{Fake: clocktest.New(), step: step}),
		syncwatch.WithLogger(log.Logger()),
		syncwatch.WithThreshold(100*time.Millisecond),
		syncwatch.WithAttrs(slog.String("database", "orders")),
	)
	s, err := New(&Config{DataDir: "/db", FS: vfs.NewMem(), SyncWrites: syncWrites}, WithSyncObserver(obs))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, reg
}

func syncCount(t *testing.T, reg *prometheus.Registry) uint64 {
	t.Helper()
	if !syncwatchtest.HasFamily(t, reg, syncFamily) {
		return 0
	}
	count, _ := syncwatchtest.HistogramSample(t, reg, syncFamily, "backend", syncwatch.BackendPebble)
	return count
}

func TestSyncedWrites_EachSyncIsObserved(t *testing.T) {
	s, reg := syncedStore(t, true, time.Millisecond, syncwatchtest.NewLog())

	if err := s.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if ok, err := s.CompareAndSwap(context.Background(), []byte("k"), []byte("v"), []byte("w")); err != nil || !ok {
		t.Fatalf("CompareAndSwap = %v, %v", ok, err)
	}
	if err := s.Delete([]byte("k")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	b := s.Batch()
	if err := b.Set([]byte("b"), []byte("1")); err != nil {
		t.Fatalf("batch Set: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("batch Commit: %v", err)
	}
	if err := s.DeleteRange([]byte("a"), []byte("c")); err != nil {
		t.Fatalf("DeleteRange: %v", err)
	}

	count, sum := syncwatchtest.HistogramSample(t, reg, syncFamily, "backend", syncwatch.BackendPebble)
	if count != 5 {
		t.Fatalf("sync count = %d, want 5 (Set, Delete, CompareAndSwap, batch Commit, DeleteRange)", count)
	}
	if want := (5 * time.Millisecond).Seconds(); sum != want {
		t.Fatalf("sync sum = %v, want %v", sum, want)
	}
}

func TestUnsyncedWrites_AreNotObserved(t *testing.T) {
	s, reg := syncedStore(t, false, time.Millisecond, syncwatchtest.NewLog())

	if err := s.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	b := s.Batch()
	if err := b.Set([]byte("b"), []byte("1")); err != nil {
		t.Fatalf("batch Set: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("batch Commit: %v", err)
	}
	if err := s.DeleteRange([]byte("a"), []byte("c")); err != nil {
		t.Fatalf("DeleteRange: %v", err)
	}

	if n := syncCount(t, reg); n != 0 {
		t.Fatalf("sync count = %d for NoSync writes, want 0", n)
	}
}

func TestSlowSync_WarnsOnceWithBackendAndDatabase(t *testing.T) {
	log := syncwatchtest.NewLog()
	s, _ := syncedStore(t, true, 250*time.Millisecond, log)

	if err := s.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	recs := log.Records()
	if len(recs) != 1 {
		t.Fatalf("got %d warnings, want exactly 1", len(recs))
	}
	r := recs[0]
	if r.Level != slog.LevelWarn || r.Attrs["took"] != 250*time.Millisecond ||
		r.Attrs["threshold"] != 100*time.Millisecond ||
		r.Attrs["backend"] != syncwatch.BackendPebble || r.Attrs["database"] != "orders" {
		t.Fatalf("warning = %+v", r)
	}
}

func TestFastSync_DoesNotWarn(t *testing.T) {
	log := syncwatchtest.NewLog()
	s, _ := syncedStore(t, true, 5*time.Millisecond, log)

	if err := s.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if n := len(log.Records()); n != 0 {
		t.Fatalf("got %d warnings for a 5ms sync, want 0", n)
	}
}

func TestWithSyncObserver_NilKeepsDefault(t *testing.T) {
	s, err := New(&Config{DataDir: "/db", FS: vfs.NewMem()}, WithSyncObserver(nil))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()
	if s.syncs == nil {
		t.Fatal("WithSyncObserver(nil) removed the default observer")
	}

	db := NewFromDB(s.db, true, WithSyncObserver(nil))
	if db.syncs == nil {
		t.Fatal("NewFromDB has no default observer")
	}
}

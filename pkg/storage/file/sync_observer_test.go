// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package file

import (
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch/syncwatchtest"
)

const syncFamily = "qrdb_storage_sync_duration_seconds"

// steppingClock is a fake clock that moves forward by step every time Now is
// read, so an fsync timed between two reads takes exactly step.
type steppingClock struct {
	*clocktest.Fake
	step time.Duration
}

func (c *steppingClock) Now() time.Time {
	now := c.Fake.Now()
	c.Advance(c.step)
	return now
}

// observedEngine opens an engine whose every fsync takes step, logging to
// log and recording into a fresh process registry it returns.
func observedEngine(t *testing.T, step time.Duration, log *syncwatchtest.Log) (*Engine, *prometheus.Registry) {
	t.Helper()
	reg := syncwatchtest.FreshRegistry(t)
	obs := syncwatch.ForStorage(syncwatch.BackendFile,
		syncwatch.WithClock(&steppingClock{Fake: clocktest.New(), step: step}),
		syncwatch.WithLogger(log.Logger()),
		syncwatch.WithThreshold(100*time.Millisecond),
		syncwatch.WithAttrs(slog.String("database", "orders")),
	)
	e, err := New(t.TempDir(), WithSyncObserver(obs))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e, reg
}

func syncCount(t *testing.T, reg *prometheus.Registry) uint64 {
	t.Helper()
	if !syncwatchtest.HasFamily(t, reg, syncFamily) {
		return 0
	}
	count, _ := syncwatchtest.HistogramSample(t, reg, syncFamily, "backend", syncwatch.BackendFile)
	return count
}

func TestSyncObserver_WritePathFsyncsAreObserved(t *testing.T) {
	e, reg := observedEngine(t, time.Millisecond, syncwatchtest.NewLog())

	steps := []struct {
		name string
		run  func() error
	}{
		{"Set", func() error { return e.Set([]byte("k"), []byte("v")) }},
		{"Delete", func() error { return e.Delete([]byte("k")) }},
		{"Sync", e.Sync},
		{"batch Commit", func() error {
			b := e.Batch()
			if err := b.Set([]byte("b"), []byte("1")); err != nil {
				return err
			}
			return b.Commit()
		}},
	}
	for _, s := range steps {
		before := syncCount(t, reg)
		if err := s.run(); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if after := syncCount(t, reg); after <= before {
			t.Fatalf("%s observed no fsync: count %d -> %d", s.name, before, after)
		}
	}
	count, sum := syncwatchtest.HistogramSample(t, reg, syncFamily, "backend", syncwatch.BackendFile)
	if want := (time.Duration(count) * time.Millisecond).Seconds(); math.Abs(sum-want) > 1e-9 {
		t.Fatalf("sum = %v over %d fsyncs of 1ms, want %v", sum, count, want)
	}
}

func TestSyncObserver_SlowFsyncWarnsWithBackendAndDatabase(t *testing.T) {
	log := syncwatchtest.NewLog()
	e, _ := observedEngine(t, 250*time.Millisecond, log)
	before := len(log.Records())

	if err := e.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	recs := log.Records()[before:]
	if len(recs) != 1 {
		t.Fatalf("got %d warnings for one slow fsync, want exactly 1", len(recs))
	}
	r := recs[0]
	if r.Level != slog.LevelWarn || r.Attrs["took"] != 250*time.Millisecond ||
		r.Attrs["threshold"] != 100*time.Millisecond ||
		r.Attrs["backend"] != syncwatch.BackendFile || r.Attrs["database"] != "orders" {
		t.Fatalf("warning = %+v", r)
	}
}

func TestSyncObserver_FastFsyncDoesNotWarn(t *testing.T) {
	log := syncwatchtest.NewLog()
	e, _ := observedEngine(t, 5*time.Millisecond, log)

	if err := e.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if n := len(log.Records()); n != 0 {
		t.Fatalf("got %d warnings for 5ms fsyncs, want 0", n)
	}
}

func TestWithSyncObserver_NilKeepsDefault(t *testing.T) {
	syncwatchtest.FreshRegistry(t)
	e, err := New(t.TempDir(), WithSyncObserver(nil))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer e.Close()
	if e.syncs == nil {
		t.Fatal("WithSyncObserver(nil) removed the default observer")
	}
}

// An Engine built by hand has no observer; its fsyncs still run.
func TestTimedSync_NilObserverStillSyncs(t *testing.T) {
	e := &Engine{}
	ran := false
	if err := e.timedSync(func() error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("timedSync = %v, ran %v", err, ran)
	}
}

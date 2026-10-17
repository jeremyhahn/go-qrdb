// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package syncwatch

import (
	"log/slog"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch/syncwatchtest"
)

func TestForStorage_SinceRecordsHistogram(t *testing.T) {
	reg := syncwatchtest.FreshRegistry(t)
	clock := clocktest.New()
	o := ForStorage(BackendPebble, WithClock(clock), WithLogger(syncwatchtest.NewLog().Logger()))

	start := o.Now()
	clock.Advance(3 * time.Millisecond)
	o.Since(start)

	count, sum := syncwatchtest.HistogramSample(t, reg, "qrdb_storage_sync_duration_seconds", "backend", BackendPebble)
	if count != 1 {
		t.Fatalf("sample count = %d, want 1", count)
	}
	if want := (3 * time.Millisecond).Seconds(); sum != want {
		t.Fatalf("sample sum = %v, want %v", sum, want)
	}
}

func TestForStorage_LabelsBackendsSeparately(t *testing.T) {
	reg := syncwatchtest.FreshRegistry(t)
	log := syncwatchtest.NewLog()
	ForStorage(BackendPebble, WithLogger(log.Logger())).Observe(time.Millisecond)
	ForStorage(BackendFile, WithLogger(log.Logger())).Observe(time.Millisecond)
	ForStorage(BackendFile, WithLogger(log.Logger())).Observe(time.Millisecond)

	if count, _ := syncwatchtest.HistogramSample(t, reg, "qrdb_storage_sync_duration_seconds", "backend", BackendPebble); count != 1 {
		t.Fatalf("pebble count = %d, want 1", count)
	}
	if count, _ := syncwatchtest.HistogramSample(t, reg, "qrdb_storage_sync_duration_seconds", "backend", BackendFile); count != 2 {
		t.Fatalf("file count = %d, want 2", count)
	}
}

func TestForStorage_SlowSyncWarnsOnceWithFields(t *testing.T) {
	syncwatchtest.FreshRegistry(t)
	log := syncwatchtest.NewLog()
	clock := clocktest.New()
	o := ForStorage(BackendFile,
		WithClock(clock),
		WithLogger(log.Logger()),
		WithThreshold(100*time.Millisecond),
		WithAttrs(slog.String("database", "orders"), slog.String("dir", "/data/orders")),
	)

	start := o.Now()
	clock.Advance(250 * time.Millisecond)
	o.Since(start)

	recs := log.Records()
	if len(recs) != 1 {
		t.Fatalf("got %d log records, want exactly 1", len(recs))
	}
	r := recs[0]
	if r.Level != slog.LevelWarn || r.Message != msgSlowStorageSync {
		t.Fatalf("record = %v %q, want WARN %q", r.Level, r.Message, msgSlowStorageSync)
	}
	want := map[string]any{
		"threshold": 100 * time.Millisecond,
		"took":      250 * time.Millisecond,
		"backend":   BackendFile,
		"database":  "orders",
		"dir":       "/data/orders",
	}
	for k, v := range want {
		if got, ok := r.Attrs[k]; !ok || got != v {
			t.Fatalf("attr %q = %v (present %v), want %v", k, got, ok, v)
		}
	}
}

func TestForStorage_FastSyncDoesNotWarn(t *testing.T) {
	syncwatchtest.FreshRegistry(t)
	log := syncwatchtest.NewLog()
	o := ForStorage(BackendPebble, WithLogger(log.Logger()), WithThreshold(100*time.Millisecond))

	o.Observe(99 * time.Millisecond)
	o.Observe(100 * time.Millisecond)

	if n := len(log.Records()); n != 0 {
		t.Fatalf("got %d log records for syncs at or under the threshold, want 0", n)
	}
}

func TestForWAL_SlowFsyncWarnsWithoutRecording(t *testing.T) {
	reg := syncwatchtest.FreshRegistry(t)
	log := syncwatchtest.NewLog()
	o := ForWAL(WithLogger(log.Logger()), WithAttrs(slog.String("wal_dir", "/data/wal")))

	o.Observe(2 * time.Second)
	o.Observe(10 * time.Millisecond)

	recs := log.Records()
	if len(recs) != 1 {
		t.Fatalf("got %d log records, want exactly 1", len(recs))
	}
	r := recs[0]
	if r.Message != msgSlowWALFsync {
		t.Fatalf("message = %q, want %q", r.Message, msgSlowWALFsync)
	}
	if r.Attrs["threshold"] != DefaultSlowThreshold || r.Attrs["took"] != 2*time.Second || r.Attrs["wal_dir"] != "/data/wal" {
		t.Fatalf("attrs = %v", r.Attrs)
	}
	if syncwatchtest.HasFamily(t, reg, "qrdb_storage_sync_duration_seconds") {
		t.Fatal("ForWAL recorded into the storage sync histogram")
	}
}

func TestOptions_DefaultsAndIgnoredValues(t *testing.T) {
	o := ForWAL(WithThreshold(0), WithThreshold(-time.Second), WithClock(nil))
	if got := o.Threshold(); got != DefaultSlowThreshold {
		t.Fatalf("threshold = %v, want default %v", got, DefaultSlowThreshold)
	}
	if o.Now().IsZero() {
		t.Fatal("a nil clock replaced the default clock")
	}

	o = ForWAL(WithThreshold(5 * time.Second))
	if got := o.Threshold(); got != 5*time.Second {
		t.Fatalf("threshold = %v, want 5s", got)
	}
}

func TestObserve_NilLoggerUsesDefault(t *testing.T) {
	log := syncwatchtest.NewLog()
	prev := slog.Default()
	slog.SetDefault(log.Logger())
	t.Cleanup(func() { slog.SetDefault(prev) })

	ForWAL().Observe(2 * DefaultSlowThreshold)

	if n := len(log.Records()); n != 1 {
		t.Fatalf("got %d records on the default logger, want 1", n)
	}
}

func TestObserver_NilIsNoop(t *testing.T) {
	var o *Observer
	if !o.Now().IsZero() {
		t.Fatal("nil Now returned a non-zero time")
	}
	o.Since(time.Now())
	o.Observe(time.Hour)
	if got := o.Threshold(); got != DefaultSlowThreshold {
		t.Fatalf("nil threshold = %v, want %v", got, DefaultSlowThreshold)
	}
}

func TestSetSlowThreshold_ObserversWithoutThresholdFollowIt(t *testing.T) {
	syncwatchtest.FreshRegistry(t)
	keepSlowThreshold(t)
	log := syncwatchtest.NewLog()
	storage := ForStorage(BackendFile, WithLogger(log.Logger()))
	wal := ForWAL(WithLogger(log.Logger()))

	SetSlowThreshold(50 * time.Millisecond)

	if got := SlowThreshold(); got != 50*time.Millisecond {
		t.Fatalf("SlowThreshold() = %v, want 50ms", got)
	}
	for _, o := range []*Observer{storage, wal} {
		if got := o.Threshold(); got != 50*time.Millisecond {
			t.Fatalf("Threshold() = %v, want the process threshold 50ms", got)
		}
		o.Observe(10 * time.Millisecond)
		o.Observe(100 * time.Millisecond)
	}
	recs := log.Records()
	if len(recs) != 2 {
		t.Fatalf("got %d log records, want one per 100ms sync", len(recs))
	}
	for i, msg := range []string{msgSlowStorageSync, msgSlowWALFsync} {
		if recs[i].Message != msg || recs[i].Attrs["threshold"] != 50*time.Millisecond || recs[i].Attrs["took"] != 100*time.Millisecond {
			t.Fatalf("record %d = %q %v, want %q at threshold 50ms took 100ms", i, recs[i].Message, recs[i].Attrs, msg)
		}
	}
}

func TestSetSlowThreshold_WithThresholdPinsTheObserver(t *testing.T) {
	keepSlowThreshold(t)
	log := syncwatchtest.NewLog()
	o := ForWAL(WithLogger(log.Logger()), WithThreshold(5*time.Second))

	SetSlowThreshold(50 * time.Millisecond)
	o.Observe(time.Second)

	if got := o.Threshold(); got != 5*time.Second {
		t.Fatalf("Threshold() = %v, want the pinned 5s", got)
	}
	if n := len(log.Records()); n != 0 {
		t.Fatalf("got %d log records under a pinned 5s threshold, want 0", n)
	}
}

func TestSetSlowThreshold_NonPositiveRestoresDefault(t *testing.T) {
	keepSlowThreshold(t)
	for _, d := range []time.Duration{0, -time.Second} {
		SetSlowThreshold(50 * time.Millisecond)
		SetSlowThreshold(d)
		if got := SlowThreshold(); got != DefaultSlowThreshold {
			t.Fatalf("SetSlowThreshold(%v): SlowThreshold() = %v, want %v", d, got, DefaultSlowThreshold)
		}
	}
	SetSlowThreshold(50 * time.Millisecond)
	var o *Observer
	if got := o.Threshold(); got != 50*time.Millisecond {
		t.Fatalf("nil Threshold() = %v, want the process threshold 50ms", got)
	}
}

// keepSlowThreshold restores the process threshold when t ends.
func keepSlowThreshold(t *testing.T) {
	t.Helper()
	prev := SlowThreshold()
	t.Cleanup(func() { SetSlowThreshold(prev) })
}

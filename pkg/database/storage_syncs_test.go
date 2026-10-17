// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package database

import (
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch/syncwatchtest"
)

// captureDefaultLog routes the default slog logger, which an observer
// without WithLogger warns through, to a capturing Log for the test.
func captureDefaultLog(t *testing.T) *syncwatchtest.Log {
	t.Helper()
	log := syncwatchtest.NewLog()
	prev := slog.Default()
	slog.SetDefault(log.Logger())
	t.Cleanup(func() { slog.SetDefault(prev) })
	return log
}

func TestStorageSyncs_SlowSyncNamesDatabaseAndDir(t *testing.T) {
	syncwatchtest.FreshRegistry(t)
	log := captureDefaultLog(t)

	storageSyncs(syncwatch.BackendFile, "orders", "/data/orders").Observe(2 * syncwatch.DefaultSlowThreshold)

	recs := log.Records()
	if len(recs) != 1 {
		t.Fatalf("got %d warnings, want exactly 1", len(recs))
	}
	got := recs[0].Attrs
	if got["backend"] != syncwatch.BackendFile || got["database"] != "orders" || got["dir"] != "/data/orders" {
		t.Fatalf("warning attrs = %v", got)
	}
}

func TestOpenStorage_FileSyncsRecordUnderFile(t *testing.T) {
	reg := syncwatchtest.FreshRegistry(t)
	captureDefaultLog(t)

	s, err := openStorage("orders", BackendFile, t.TempDir())
	if err != nil {
		t.Fatalf("openStorage: %v", err)
	}
	defer s.Close()
	if err := s.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if count, _ := syncwatchtest.HistogramSample(t, reg, "qrdb_storage_sync_duration_seconds", "backend", syncwatch.BackendFile); count == 0 {
		t.Fatal("a file database opened by openStorage records no syncs")
	}
}

func TestOpenStorage_PebbleRegistersPebbleSeries(t *testing.T) {
	reg := syncwatchtest.FreshRegistry(t)

	s, err := openStorage("orders", BackendPebble, t.TempDir())
	if err != nil {
		t.Fatalf("openStorage: %v", err)
	}
	defer s.Close()

	// DefaultConfig opens without SyncWrites, so no sync is recorded, but
	// the series exists from open: a scrape shows the backend at zero.
	if count, _ := syncwatchtest.HistogramSample(t, reg, "qrdb_storage_sync_duration_seconds", "backend", syncwatch.BackendPebble); count != 0 {
		t.Fatalf("sync count = %d, want 0", count)
	}
}

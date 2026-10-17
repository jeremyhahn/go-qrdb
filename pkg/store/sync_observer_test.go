// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package store

import (
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch/syncwatchtest"
)

const syncFamily = "qrdb_storage_sync_duration_seconds"

func memDB(t *testing.T) *pebble.DB {
	t.Helper()
	db, err := pebble.Open("/sm", &pebble.Options{FS: vfs.NewMem()})
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestPebbleSyncs_SyncedWrapperRecordsUnderPebble(t *testing.T) {
	reg := syncwatchtest.FreshRegistry(t)
	s := pebbleStorage.NewFromDB(memDB(t), true, pebbleSyncs("/data/shard-7"))

	if err := s.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if count, _ := syncwatchtest.HistogramSample(t, reg, syncFamily, "backend", syncwatch.BackendPebble); count != 1 {
		t.Fatalf("sync count = %d, want 1", count)
	}
}

func TestPebbleSyncs_NoSyncWrapperRecordsNothing(t *testing.T) {
	reg := syncwatchtest.FreshRegistry(t)
	s := pebbleStorage.NewFromDB(memDB(t), false, pebbleSyncs("/data/shard-7"))

	if err := s.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if count, _ := syncwatchtest.HistogramSample(t, reg, syncFamily, "backend", syncwatch.BackendPebble); count != 0 {
		t.Fatalf("sync count = %d for a NoSync write, want 0", count)
	}
}

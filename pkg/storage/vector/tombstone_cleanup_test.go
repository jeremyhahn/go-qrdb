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
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector/schema"
)

// storeWithTombstone opens an in-memory store holding one deleted vector, so
// exactly one tombstone sits under namespace "docs".
func storeWithTombstone(t *testing.T) *PebbleVectorStorage {
	t.Helper()
	s, err := NewPebbleVectorStorage(DefaultConfig("/vec"), &testLogger{}, inMemory)
	if err != nil {
		t.Fatalf("NewPebbleVectorStorage: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	if err := s.CreateNamespace(ctx, &NamespaceInfo{Name: "docs", Dimension: 4}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	if err := s.UpsertVector(ctx, "docs", &VectorValue{ID: 1, Version: 1, Embedding: []float32{1, 2, 3, 4}}); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	if err := s.DeleteVector(ctx, "docs", 1); err != nil {
		t.Fatalf("DeleteVector: %v", err)
	}
	if n := tombstones(t, s); n != 1 {
		t.Fatalf("tombstones after DeleteVector = %d, want 1", n)
	}
	return s
}

func tombstones(t *testing.T, s *PebbleVectorStorage) int {
	t.Helper()
	prefix := schema.EncodeTombstonePrefix("docs")
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: schema.PrefixSuccessor(prefix)})
	if err != nil {
		t.Fatalf("NewIter: %v", err)
	}
	defer iter.Close()
	n := 0
	for iter.First(); iter.Valid(); iter.Next() {
		n++
	}
	return n
}

func TestCleanupTombstones_CollectsTombstonesOlderThanTheCutoff(t *testing.T) {
	s := storeWithTombstone(t)

	// A negative age puts the cutoff an hour ahead, so the tombstone just
	// written is older than it.
	if err := s.CleanupTombstones(context.Background(), -time.Hour); err != nil {
		t.Fatalf("CleanupTombstones: %v", err)
	}
	if n := tombstones(t, s); n != 0 {
		t.Fatalf("tombstones after cleanup = %d, want 0", n)
	}
}

func TestCleanupTombstones_KeepsTombstonesYoungerThanTheCutoff(t *testing.T) {
	s := storeWithTombstone(t)

	if err := s.CleanupTombstones(context.Background(), time.Hour); err != nil {
		t.Fatalf("CleanupTombstones: %v", err)
	}
	if n := tombstones(t, s); n != 1 {
		t.Fatalf("tombstones after cleanup = %d, want 1", n)
	}
}

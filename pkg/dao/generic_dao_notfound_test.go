// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package dao

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

func newNotFoundEntity() *TestEntity { return &TestEntity{} }

// A miss over the memory store is NOT FOUND, not corruption.
//
// kvstore.KVStore.Get documents "(nil, nil) when key does not exist", and
// MemoryKVStore honours it, so Get used to see a nil error, fall through to the
// codec with zero bytes, and report:
//
//	dao: failed to deserialize test_entity: unexpected end of JSON input
//
// A DeserializationFailedError for an ordinary absence. IsNotFound was false,
// so a consumer had no typed way to tell "no such record" from "the store is
// broken" — over the store the SDK documents as the supported way to unit-test
// DAO code, which makes it the first thing every consumer meets (go-qrdb#58).
func TestGet_AMissOverTheMemoryStoreIsNotFound(t *testing.T) {
	ctx := context.Background()

	d, err := New(kvstore.NewMemoryKVStore(), "test_entity", newNotFoundEntity)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = d.Get(ctx, 404)
	if err == nil {
		t.Fatal("Get on an id that was never saved returned no error")
	}
	if !IsNotFound(err) {
		t.Fatalf("Get on an absent id = %v (%T); IsNotFound must be true, or a "+
			"consumer cannot distinguish an absent record from a broken store", err, err)
	}
}

// The assertion that keeps the fix honest: genuinely damaged bytes must NOT be
// reported as absence.
//
// Mapping the deserialization failure back to not-found would have made this
// test pass by hiding real corruption — and that is strictly worse than the
// defect, because a caller told a record is absent creates one and overwrites
// whatever the damaged bytes held. So the two outcomes are pinned together: an
// empty read is absence, a non-empty read that will not decode is not.
func TestGet_CorruptBytesAreNotReportedAsAbsence(t *testing.T) {
	ctx := context.Background()
	store := kvstore.NewMemoryKVStore()

	d, err := New(store, "test_entity", newNotFoundEntity)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Bytes that exist and cannot be decoded — the store is not lying about
	// having them, so this is corruption and must say so.
	if perr := store.Put(ctx, "test_entity/7", []byte("{not json")); perr != nil {
		t.Fatalf("Put: %v", perr)
	}

	_, err = d.Get(ctx, 7)
	if err == nil {
		t.Fatal("Get over corrupt bytes returned no error")
	}
	if IsNotFound(err) {
		t.Fatalf("Get over corrupt bytes = %v; IsNotFound must be FALSE — reporting "+
			"damage as absence lets the caller conclude the record must be created "+
			"and overwrite it", err)
	}
	var deserialization DeserializationFailedError
	if !errors.As(err, &deserialization) {
		t.Fatalf("error = %T (%v), want DeserializationFailedError", err, err)
	}
}

// A STORED empty value is damage, not absence — and the two are distinguishable,
// which is why the check is `data == nil` rather than `len(data) == 0`.
//
// MemoryKVStore returns a non-nil zero-length slice for a stored empty value and
// nil only for a key it does not hold. Measured:
//
//	stored empty  -> nil=false len=0
//	absent        -> nil=true  len=0
//
// Testing len() would report the first as absent, and a create-on-miss caller
// would then overwrite it. That is the failure this whole change exists to
// avoid, so it must not be reintroduced by the fix for it.
func TestGet_AStoredEmptyValueIsCorruptionNotAbsence(t *testing.T) {
	ctx := context.Background()
	store := kvstore.NewMemoryKVStore()

	d, err := New(store, "test_entity", newNotFoundEntity)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if perr := store.Put(ctx, "test_entity/9", []byte{}); perr != nil {
		t.Fatalf("Put: %v", perr)
	}

	_, err = d.Get(ctx, 9)
	if err == nil {
		t.Fatal("Get over a stored empty value returned no error")
	}
	if IsNotFound(err) {
		t.Fatalf("Get over a STORED empty value = %v; IsNotFound must be FALSE — the "+
			"record exists and is damaged, and a caller told it is absent creates "+
			"one over the top", err)
	}
	var deserialization DeserializationFailedError
	if !errors.As(err, &deserialization) {
		t.Fatalf("error = %T (%v), want DeserializationFailedError", err, err)
	}
}

// A record that round-trips is unaffected: the absence arm must not swallow
// a real entity.
func TestGet_APresentRecordStillReads(t *testing.T) {
	ctx := context.Background()

	d, err := New(kvstore.NewMemoryKVStore(), "test_entity", newNotFoundEntity)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	saved := &TestEntity{ID: 11, Name: "acme", Email: "a@example.com"}
	if serr := d.Save(ctx, saved); serr != nil {
		t.Fatalf("Save: %v", serr)
	}

	got, err := d.Get(ctx, 11)
	if err != nil {
		t.Fatalf("Get on a saved id: %v", err)
	}
	if got.Name != "acme" {
		t.Fatalf("Name = %q, want %q", got.Name, "acme")
	}
}

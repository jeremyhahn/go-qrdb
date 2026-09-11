// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// closed_parity_test.go is the differential oracle for one question: what does
// each storage backend do when an operation arrives after Close?
//
// Before go-qrdb#371 the four backends gave three different answers, and the
// PebbleDB one was a panic — `pebble: closed`, raised several frames inside
// Commit, with no typed value to match. A consumer had to recover it and
// classify by string. This file pins all four to the same answer.
package storage_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// newClosedBackends returns one instance of each backend, already populated
// with a key and already closed.
func newClosedBackends(t *testing.T) []namedBackend {
	t.Helper()

	pdb, err := pebble.New(&pebble.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("pebble.New: %v", err)
	}
	fdb, err := file.New(t.TempDir())
	if err != nil {
		t.Fatalf("file.New: %v", err)
	}
	odb, err := objstore.New("local", map[string]string{"path": t.TempDir()})
	if err != nil {
		t.Fatalf("objstore.New: %v", err)
	}

	backends := []namedBackend{
		{"memory", memory.NewStorage()},
		{"file", fdb},
		{"objstore", odb},
		{"pebble", pdb},
	}
	for _, b := range backends {
		if err := b.s.Set([]byte("k"), []byte("v")); err != nil {
			t.Fatalf("%s: seed Set: %v", b.name, err)
		}
		if err := b.s.Close(); err != nil {
			t.Fatalf("%s: Close: %v", b.name, err)
		}
	}
	return backends
}

// closedOperations are the calls a caller can still make on a closed engine.
// Each returns the error the caller would see; a panic is caught by the
// harness and reported as the failure it is.
var closedOperations = []struct {
	name string
	call func(s storage.Storage) error
}{
	{"Get", func(s storage.Storage) error { _, err := s.Get([]byte("k")); return err }},
	{"Set", func(s storage.Storage) error { return s.Set([]byte("k"), []byte("v")) }},
	{"Delete", func(s storage.Storage) error { return s.Delete([]byte("k")) }},
	{"List", func(s storage.Storage) error { _, err := s.List(nil); return err }},
	{"Exists", func(s storage.Storage) error { _, err := s.Exists([]byte("k")); return err }},
	{"Snapshot", func(s storage.Storage) error { _, err := s.Snapshot(); return err }},
	{"Batch.Set", func(s storage.Storage) error { return s.Batch().Set([]byte("k"), []byte("v")) }},
	{"Batch.Delete", func(s storage.Storage) error { return s.Batch().Delete([]byte("k")) }},
	{"Batch.Get", func(s storage.Storage) error { _, err := s.Batch().Get([]byte("k")); return err }},
	{"Batch.Commit", func(s storage.Storage) error {
		b := s.Batch()
		_ = b.Set([]byte("k"), []byte("v"))
		return b.Commit()
	}},
	{"Batch.Rollback", func(s storage.Storage) error { return s.Batch().Rollback() }},
	{"Batch.Iterate", func(s storage.Storage) error {
		it := s.Batch().NewIterator(nil)
		it.SeekToFirst()
		return it.Error()
	}},
	{"Iterate", func(s storage.Storage) error {
		it := s.NewIterator(nil)
		it.SeekToFirst()
		return it.Error()
	}},
}

// callClosed runs one operation and converts a panic into an error, so a
// backend that panics fails the assertion instead of killing the test binary —
// which is exactly what it does to a daemon.
func callClosed(call func(storage.Storage) error, s storage.Storage) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("PANICKED: %v", r)
		}
	}()
	return call(s)
}

// TestClosedStorageParity asserts that every backend answers every operation
// on a closed engine with an error that matches storage.ErrStorageClosed, and
// that none of them panics.
//
// On dev/main this failed on pebble at Batch.Get, Batch.Commit and Iterate
// with "PANICKED: pebble: closed" — the defect go-qrdb#371 reports.
func TestClosedStorageParity(t *testing.T) {
	for _, b := range newClosedBackends(t) {
		for _, op := range closedOperations {
			t.Run(b.name+"/"+op.name, func(t *testing.T) {
				err := callClosed(op.call, b.s)
				if err == nil {
					t.Fatalf("%s on a closed %s returned nil, want ErrStorageClosed",
						op.name, b.name)
				}
				if !errors.Is(err, storage.ErrStorageClosed) {
					t.Fatalf("%s on a closed %s: errors.Is(%v, ErrStorageClosed) = false, want true",
						op.name, b.name, err)
				}
				if !storage.IsClosed(err) {
					t.Fatalf("%s on a closed %s: storage.IsClosed(%v) = false, want true",
						op.name, b.name, err)
				}
			})
		}
	}
}

// TestClosedStorageIteratorIsNotEmptyRange asserts a closed engine's iterator
// reports the closure rather than passing for an empty key range. A scan that
// silently returns "no keys" from a closed store is a correctness bug of a
// worse kind than the panic: it is a wrong answer, not a loud one.
func TestClosedStorageIteratorIsNotEmptyRange(t *testing.T) {
	for _, b := range newClosedBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			it := b.s.NewIterator(nil)
			defer func() { _ = it.Close() }()

			if it.SeekToFirst() {
				t.Error("SeekToFirst on a closed engine positioned the iterator")
			}
			if it.Valid() {
				t.Error("Valid on a closed engine reported a position")
			}
			if !errors.Is(it.Error(), storage.ErrStorageClosed) {
				t.Errorf("Error() = %v, want ErrStorageClosed", it.Error())
			}
		})
	}
}

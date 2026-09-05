// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// go-qrdb#268: this engine's directory fsync was always correct. What was NOT
// guarded is that it stays the same fsync as everywhere else -- it was a
// private second copy of the primitive, and a private copy is what drifts.
// These tests pin the engine to the shared implementation by failing it at the
// shared seam: they pass only while this package routes through
// pkg/util/fsync, so restoring a local copy fails them.
package file

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/util/fsync"
)

// TestSet_RoutesItsDirectorySyncThroughTheSharedPrimitive.
func TestSet_RoutesItsDirectorySyncThroughTheSharedPrimitive(t *testing.T) {
	e, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	sentinel := errors.New("directory sync refused")
	t.Cleanup(fsync.SetForTest(func(string) error { return sentinel }))

	if err := e.Set([]byte("k"), []byte("v")); !errors.Is(err, sentinel) {
		t.Fatalf("Set = %v; want the shared primitive's sync failure. If this passes, the "+
			"engine has stopped using pkg/util/fsync and has a private copy of the "+
			"directory fsync again", err)
	}
}

// TestBatch_RoutesItsJournalDirectorySyncThroughTheSharedPrimitive: the
// journal's publish is the batch's commit point, and it must be durable
// through the same one implementation.
func TestBatch_RoutesItsJournalDirectorySyncThroughTheSharedPrimitive(t *testing.T) {
	e, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	sentinel := errors.New("directory sync refused")
	t.Cleanup(fsync.SetForTest(func(string) error { return sentinel }))

	if err := e.writeJournal([]byte("image")); !errors.Is(err, sentinel) {
		t.Fatalf("writeJournal = %v; want the shared primitive's sync failure", err)
	}
}

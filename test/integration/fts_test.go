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

//go:build integration && qrdb_fts

package integration_test

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/fts"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	filestorage "github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	pebblestorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// ---------------------------------------------------------------------------
// full-text-search: FTSIndex over every production storage.Storage backend
// ---------------------------------------------------------------------------
//
// pkg/fts has no CLI, SDK, or transport surface (docs/full-text-search/cli.md
// records this as a known gap, not a design choice here to close), so there
// is no multi-protocol table to drive. Per test/integration/CLAUDE.md's
// "packages without CLI support" convention, the upper-most abstraction --
// FTSIndex -- is exercised directly instead. pkg/fts's own unit tests already
// prove the inverted-index algorithm is correct against one backend
// (Pebble); what belongs here, in integration, is proof that FTSIndex
// behaves identically across EVERY storage.Storage a qrdbd deployment can
// run on, since fts: keys are ordinary entries replicated through whichever
// engine the cluster was configured with.

// ftsBackend names one of the production storage.Storage constructors this
// suite runs FTSIndex against.
type ftsBackend struct {
	name string
	open func(t *testing.T) storage.Storage
}

var ftsBackends = []ftsBackend{
	{
		name: "memory",
		open: func(t *testing.T) storage.Storage {
			t.Helper()
			return memory.NewStorage()
		},
	},
	{
		name: "file",
		open: func(t *testing.T) storage.Storage {
			t.Helper()
			s, err := filestorage.New(t.TempDir())
			if err != nil {
				t.Fatalf("file storage: %v", err)
			}
			return s
		},
	},
	{
		name: "pebble",
		open: func(t *testing.T) storage.Storage {
			t.Helper()
			s, err := pebblestorage.New(pebblestorage.TestConfig(t.TempDir()))
			if err != nil {
				t.Fatalf("pebble storage: %v", err)
			}
			return s
		},
	},
}

// closeStorage closes s if it implements io.Closer-like Close() error,
// which every storage.Storage implementation here does.
func closeFTSStorage(t *testing.T, s storage.Storage) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatalf("close storage: %v", err)
	}
}

// TestFTSIndex_PerBackend_PublicAPI proves IndexDocument/Search/SearchOr/
// RemoveDocument behave identically on every production storage backend:
// an AND search narrows to the document containing all terms, an OR search
// returns every document containing any term, and removing a document drops
// it from subsequent searches -- on memory, file, and Pebble storage alike.
func TestFTSIndex_PerBackend_PublicAPI(t *testing.T) {
	for _, backend := range ftsBackends {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			s := backend.open(t)
			defer closeFTSStorage(t, s)

			index := fts.NewFTSIndex(s, "messages.body")

			if err := index.IndexDocument([]byte("msg/1"), "hello world"); err != nil {
				t.Fatalf("[%s] index msg/1: %v", backend.name, err)
			}
			if err := index.IndexDocument([]byte("msg/2"), "hello there friend"); err != nil {
				t.Fatalf("[%s] index msg/2: %v", backend.name, err)
			}

			andKeys, err := index.Search("hello world")
			if err != nil {
				t.Fatalf("[%s] AND search: %v", backend.name, err)
			}
			if len(andKeys) != 1 || string(andKeys[0]) != "msg/1" {
				t.Fatalf("[%s] AND search = %v, want [msg/1]", backend.name, andKeys)
			}

			orKeys, err := index.SearchOr("hello world")
			if err != nil {
				t.Fatalf("[%s] OR search: %v", backend.name, err)
			}
			if len(orKeys) != 2 {
				t.Fatalf("[%s] OR search = %v, want 2 matches", backend.name, orKeys)
			}

			if err := index.RemoveDocument([]byte("msg/1")); err != nil {
				t.Fatalf("[%s] remove msg/1: %v", backend.name, err)
			}

			afterRemove, err := index.Search("hello")
			if err != nil {
				t.Fatalf("[%s] search after remove: %v", backend.name, err)
			}
			if len(afterRemove) != 1 || string(afterRemove[0]) != "msg/2" {
				t.Fatalf("[%s] search after remove = %v, want [msg/2]", backend.name, afterRemove)
			}
		})
	}
}

// TestFTSIndex_PerBackend_ReindexReplacesPreviousEntries proves that
// indexing the same entity key a second time replaces, rather than adds to,
// its previous index entries -- on every production storage backend. A
// search on a term from the stale text must miss once the entity has been
// reindexed with new text that no longer contains that term.
func TestFTSIndex_PerBackend_ReindexReplacesPreviousEntries(t *testing.T) {
	for _, backend := range ftsBackends {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			s := backend.open(t)
			defer closeFTSStorage(t, s)

			index := fts.NewFTSIndex(s, "messages.body")

			if err := index.IndexDocument([]byte("msg/1"), "original coffee text"); err != nil {
				t.Fatalf("[%s] initial index: %v", backend.name, err)
			}
			if err := index.IndexDocument([]byte("msg/1"), "replacement tea text"); err != nil {
				t.Fatalf("[%s] reindex: %v", backend.name, err)
			}

			staleKeys, err := index.Search("coffee")
			if err != nil {
				t.Fatalf("[%s] search stale term: %v", backend.name, err)
			}
			if len(staleKeys) != 0 {
				t.Fatalf("[%s] search stale term = %v, want no matches after reindex", backend.name, staleKeys)
			}

			freshKeys, err := index.Search("tea")
			if err != nil {
				t.Fatalf("[%s] search fresh term: %v", backend.name, err)
			}
			if len(freshKeys) != 1 || string(freshKeys[0]) != "msg/1" {
				t.Fatalf("[%s] search fresh term = %v, want [msg/1]", backend.name, freshKeys)
			}
		})
	}
}

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

package server

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// dbGuard is embedded by every namespaced view whose writes are Raft
// proposals -- KV, MVCC, batch, transaction, time-series and index -- and
// carries the write guard of the database incarnation the view is scoped
// to: its drop-fence marker holds exactly that incarnation
// (service.DropFenceGuard).
//
// Each mutating method attaches it to the request context before it
// delegates, and every proposal pkg/service makes under that context carries
// the guard in the SAME Raft command as the write (service.WriteGuard,
// protocol.OpGuarded). A Drop that fences the database therefore refuses, at
// apply time and on every replica, every write that had not applied before
// the fence -- including one resolved, checked and proposed before the drop
// began -- and, because the guard names the incarnation, every write through
// a view of an earlier database of the same name. There is no check here to
// race: the state machine decides.
//
// The zero value guards nothing, which is what a view built without a
// database scope -- a test, or a composition with nothing to drop -- gets.
type dbGuard struct {
	guard service.WriteGuard
}

// scope returns ctx carrying the view's write guard.
func (g dbGuard) scope(ctx context.Context) context.Context {
	return service.WithWriteGuard(ctx, g.guard)
}

// dropFence is the check-then-write fence the vector view still uses.
//
// Vector writes are the one namespaced write that is not a Raft proposal:
// the vector service stores embeddings in a node-local store, so there is no
// replicated command for a guard to ride on, and the check here runs before
// the write rather than inside it. See docs/databases/engineering.md for
// what that leaves open.
type dropFence struct {
	// check reports whether the database the view is scoped to is being
	// dropped. nil for a view with nothing to check.
	check func() error
}

// refuse calls the configured fence check, if any.
func (f dropFence) refuse() error {
	if f.check == nil {
		return nil
	}
	return f.check()
}

// guardedCollectionService carries a database's write guard onto a
// CollectionServicer built over engines that do not carry it themselves.
//
// A dedicated-raft database's collection service is built once, over its
// shard's raw engines, by RaftBackendFactory. Rather than rebuild it over
// guarded views on every resolution -- its ID generators make its lifetime
// observable -- the guard is attached to the context of each mutating call:
// every proposal the collection service makes underneath takes that context,
// so it carries the guard all the same.
type guardedCollectionService struct {
	dbGuard
	inner service.CollectionServicer
}

// Compile-time interface check.
var _ service.CollectionServicer = (*guardedCollectionService)(nil)

// CreateCollection creates a collection under the database's write guard.
func (s *guardedCollectionService) CreateCollection(ctx context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
	return s.inner.CreateCollection(s.scope(ctx), req)
}

// DropCollection drops a collection under the database's write guard.
func (s *guardedCollectionService) DropCollection(ctx context.Context, req *service.DropCollectionRequest) error {
	return s.inner.DropCollection(s.scope(ctx), req)
}

// ListCollections lists collections.
func (s *guardedCollectionService) ListCollections(ctx context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
	return s.inner.ListCollections(ctx, req)
}

// GetCollectionInfo returns a collection's metadata.
func (s *guardedCollectionService) GetCollectionInfo(ctx context.Context, database, name string) (*service.CollectionInfo, error) {
	return s.inner.GetCollectionInfo(ctx, database, name)
}

// InsertDocument inserts a document under the database's write guard.
func (s *guardedCollectionService) InsertDocument(ctx context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
	return s.inner.InsertDocument(s.scope(ctx), req)
}

// GetDocument reads a document.
func (s *guardedCollectionService) GetDocument(ctx context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
	return s.inner.GetDocument(ctx, req)
}

// UpdateDocument updates a document under the database's write guard.
func (s *guardedCollectionService) UpdateDocument(ctx context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
	return s.inner.UpdateDocument(s.scope(ctx), req)
}

// DeleteDocument deletes a document under the database's write guard.
func (s *guardedCollectionService) DeleteDocument(ctx context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
	return s.inner.DeleteDocument(s.scope(ctx), req)
}

// UpsertDocument inserts or replaces a document under the database's write
// guard.
func (s *guardedCollectionService) UpsertDocument(ctx context.Context, req *service.UpsertDocumentRequest) (*service.UpsertDocumentResponse, error) {
	return s.inner.UpsertDocument(s.scope(ctx), req)
}

// ListDocuments lists documents.
func (s *guardedCollectionService) ListDocuments(ctx context.Context, req *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
	return s.inner.ListDocuments(ctx, req)
}

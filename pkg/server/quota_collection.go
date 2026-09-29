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
	"errors"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// quotaCollectionService wraps a CollectionServicer to enforce a tenant's
// storage-byte quota on document writes (go-qrdb#456).
//
// Why a wrapper of its own. The collection service writes documents through
// the KV servicer it was BUILT over, and the composite one is built -- and
// cached, because it owns the process-local increment ID generators -- before
// applyQuota wraps the bundle's KV. So a document write never met the KV quota
// wrapper, and a tenant at its MaxStorageBytes limit whose KV write was
// refused could store the same bytes as a document. applyQuota builds this
// wrapper on every resolve with the tenant's CURRENT limit, around whichever
// collection service the bundle carries, so the check is per call and a
// changed limit is never pinned by the cache.
//
// Byte cost is the document BODY: the JSON encoding of the document's fields,
// "_id" excluded because the identity is not stored as a field. The collection
// metadata a write touches (schema discovery) and an increment collection's
// replicated ID counter are not counted -- they are small, per-collection
// rather than per-document, and counting them would charge a tenant for
// bookkeeping the server chose to do. That is a stated undercount, measured
// the same way for every transport, including the SQL surface.
//
// Enforcement is immediate: a tenant already over its limit through documents
// written before this accounting existed has its next document write refused.
//
// Releases mirror the KV wrapper's three-way rule (see quota_kv.go): a write
// refused or failed before it could store anything releases its reservation;
// a write whose outcome is UNKNOWN -- a storage error from the write itself --
// retains it, and so does a refused write whose document somebody else had
// already removed and accounted for (service.CollectionWriteSupersededError).
//
// Every release is of bytes the release's own guarded write removed, so
// concurrent callers never release one document twice: a delete or an update
// releases the document its guarded write removed or displaced, an insert or
// upsert that displaced a document releases that document, and a drop --
// whether a caller's or the sweeper's -- releases what its teardown reports it
// removed (service.CollectionDropMeter, service.CollectionSweepResult), which
// is counted per document by the teardown's own guarded deletes. A drop that
// fails part-way releases what it removed before failing. For a metered
// tenant an update is therefore a guarded replace, and like every guarded
// write it is synchronous.
//
// Every write it makes is guarded: it never hands a request to the service's
// unguarded update or delete, whose effect it could not measure.
//
// One direction of drift remains, and it is the fail-closed one -- the
// tenant is charged for more than is stored, never less:
//   - a write refused because its collection was dropped under it may have
//     displaced a document in the dropped incarnation first, and that
//     document's bytes are not released;
//   - a write whose outcome is unknown keeps its reservation even when it in
//     fact stored nothing;
//   - a writer that dies between its write and its fence, after the drop's
//     teardown scanned the keyspace, leaves its document charged.
//
// The reconciler corrects a volume tenant; for a prefix or raft tenant the
// over-count is permanent.
type quotaCollectionService struct {
	inner    service.CollectionServicer
	acct     *quota.Accountant
	tenantID string
	limit    int64
}

// Compile-time interface checks.
var (
	_ service.CollectionServicer  = (*quotaCollectionService)(nil)
	_ service.CollectionSweeper   = (*quotaCollectionService)(nil)
	_ service.CollectionDropMeter = (*quotaCollectionService)(nil)
)

// newQuotaCollectionService wraps inner with document storage-byte quota
// enforcement for the given tenant and limit (MaxStorageBytes).
func newQuotaCollectionService(inner service.CollectionServicer, acct *quota.Accountant, tenantID string, limit int64) *quotaCollectionService {
	return &quotaCollectionService{inner: inner, acct: acct, tenantID: tenantID, limit: limit}
}

// reservationRetained reports whether a failed document write keeps its
// reservation. It does when the write may nonetheless have stored its
// document -- the service decides that (CollectionMetadataError.OutcomeUnknown):
// a storage error from the write itself, from the fence that follows it, or
// from a failed retraction -- and when the refused write DID store a document
// that somebody else then removed and released
// (CollectionWriteSupersededError). Every other refusal is decided before
// anything is stored or after the write was definitively retracted.
func reservationRetained(err error) bool {
	var meta *service.CollectionMetadataError
	if errors.As(err, &meta) && meta.OutcomeUnknown() {
		return true
	}
	var superseded *service.CollectionWriteSupersededError
	return errors.As(err, &superseded)
}

// guardedReplaceAttempts bounds how often an update or delete re-reads a
// document that a concurrent writer keeps changing between the read that
// measures it and the guarded write that replaces or removes it.
const guardedReplaceAttempts = 8

// isDocumentConflict reports whether err is a lost guarded write.
func isDocumentConflict(err error) bool {
	var conflict *service.CollectionDocumentConflictError
	return errors.As(err, &conflict)
}

// admit reserves n bytes against the tenant's quota.
func (s *quotaCollectionService) admit(ctx context.Context, n int64) error {
	return s.acct.Reserve(ctx, s.tenantID, s.limit, n)
}

// settle releases a reservation of n bytes after a failed write whose
// outcome is known.
func (s *quotaCollectionService) settle(err error, n int64) {
	if !reservationRetained(err) {
		s.acct.Release(s.tenantID, n)
	}
}

// CreateCollection passes through; MaxCollections is enforced by the service.
func (s *quotaCollectionService) CreateCollection(ctx context.Context, req *service.CreateCollectionRequest) (*service.CollectionInfo, error) {
	return s.inner.CreateCollection(ctx, req)
}

// DropCollection drops the collection and releases the body bytes of exactly
// the documents the drop removed. See DropCollectionMetered.
func (s *quotaCollectionService) DropCollection(ctx context.Context, req *service.DropCollectionRequest) error {
	_, err := s.DropCollectionMetered(ctx, req)
	return err
}

// DropCollectionMetered drops through the wrapped service's metered drop and
// releases what it reports removing -- also when the drop fails part-way,
// since what it removed before failing is gone either way. The bytes are the
// teardown's own count of the documents its guarded deletes removed, not a
// measurement taken beforehand: a document a concurrent delete removed first
// was released by that delete and is not counted again.
//
// A wrapped service that cannot report what its drop removed is refused
// rather than dropped unmetered: releasing nothing would leave the tenant
// over-counted for good, and releasing a guess would let it exceed its limit.
func (s *quotaCollectionService) DropCollectionMetered(ctx context.Context, req *service.DropCollectionRequest) (int64, error) {
	meter, ok := s.inner.(service.CollectionDropMeter)
	if !ok {
		return 0, notImplemented("metered collection drop", "")
	}
	freed, err := meter.DropCollectionMetered(ctx, req)
	if freed > 0 {
		s.acct.Sub(s.tenantID, freed)
	}
	return freed, err
}

// ListCollections passes through.
func (s *quotaCollectionService) ListCollections(ctx context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
	return s.inner.ListCollections(ctx, req)
}

// GetCollectionInfo passes through.
func (s *quotaCollectionService) GetCollectionInfo(ctx context.Context, database, name string) (*service.CollectionInfo, error) {
	return s.inner.GetCollectionInfo(ctx, database, name)
}

// InsertDocument admits the document's body bytes before the write and, when
// the insert displaced a document -- a field_hash re-insert of an existing
// natural key -- releases exactly the document it displaced, so a re-insert
// is charged once, not once per insert.
func (s *quotaCollectionService) InsertDocument(ctx context.Context, req *service.InsertDocumentRequest) (*service.InsertDocumentResponse, error) {
	n := service.DocumentBodyBytes(req.Document)
	if err := s.admit(ctx, n); err != nil {
		return nil, err
	}
	resp, err := s.inner.InsertDocument(ctx, req)
	if err != nil {
		s.settle(err, n)
		return nil, err
	}
	if resp.Replaced {
		s.acct.Sub(s.tenantID, service.DocumentBodyBytes(resp.Previous))
	}
	return resp, nil
}

// GetDocument passes through.
func (s *quotaCollectionService) GetDocument(ctx context.Context, req *service.GetDocumentRequest) (*service.GetDocumentResponse, error) {
	return s.inner.GetDocument(ctx, req)
}

// UpdateDocument admits the new body and, once it is stored, releases the
// body of exactly the document it replaced.
//
// The release has to be of the document the write displaced, not of one read
// beforehand: two concurrent updates reading the same prior document would
// otherwise both release it. So the replace is a guarded upsert against the
// revision just read -- the displaced document is then known exactly -- and a
// lost guard re-reads and tries again.
//
// Every write this makes is guarded. A read that finds no document, or no
// active collection, is returned as the refusal it is -- the service's own
// update would refuse the same way -- rather than handed to the service's
// unguarded update: a document written between that read and the update
// would be replaced with nothing released, and the tenant charged for it
// twice, for good.
func (s *quotaCollectionService) UpdateDocument(ctx context.Context, req *service.UpdateDocumentRequest) (*service.UpdateDocumentResponse, error) {
	n := service.DocumentBodyBytes(req.Document)
	if err := s.admit(ctx, n); err != nil {
		return nil, err
	}
	for attempt := 0; attempt < guardedReplaceAttempts; attempt++ {
		current, err := s.readForGuard(ctx, req.Database, req.Collection, req.ID)
		if err != nil {
			s.settle(err, n)
			return nil, err
		}
		up, uerr := s.inner.UpsertDocument(ctx, &service.UpsertDocumentRequest{
			Database: req.Database, Collection: req.Collection, ID: req.ID,
			Document: req.Document, IfRevision: current.Revision,
		})
		if isDocumentConflict(uerr) {
			continue
		}
		if uerr != nil {
			s.settle(uerr, n)
			return nil, uerr
		}
		s.acct.Sub(s.tenantID, service.DocumentBodyBytes(up.Previous))
		return &service.UpdateDocumentResponse{ID: req.ID}, nil
	}
	conflict := &service.CollectionDocumentConflictError{Database: req.Database, Collection: req.Collection, ID: req.ID}
	s.settle(conflict, n)
	return nil, conflict
}

// readForGuard reads the document a guarded write is about to replace or
// remove. A document that is absent is CollectionDocumentNotFoundError, never
// a nil response.
func (s *quotaCollectionService) readForGuard(ctx context.Context, database, collection string, id uint64) (*service.GetDocumentResponse, error) {
	current, err := s.inner.GetDocument(ctx, &service.GetDocumentRequest{Database: database, Collection: collection, ID: id})
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, &service.CollectionDocumentNotFoundError{Database: database, Collection: collection, ID: id}
	}
	return current, nil
}

// UpsertDocument admits the new body and, when the guarded write replaced a
// document, releases the body of exactly the document it displaced.
func (s *quotaCollectionService) UpsertDocument(ctx context.Context, req *service.UpsertDocumentRequest) (*service.UpsertDocumentResponse, error) {
	n := service.DocumentBodyBytes(req.Document)
	if err := s.admit(ctx, n); err != nil {
		return nil, err
	}
	resp, err := s.inner.UpsertDocument(ctx, req)
	if err != nil {
		s.settle(err, n)
		return nil, err
	}
	if resp.Replaced {
		s.acct.Sub(s.tenantID, service.DocumentBodyBytes(resp.Previous))
	}
	return resp, nil
}

// DeleteDocument releases the body bytes of exactly the document it deleted.
//
// As for UpdateDocument, the delete is guarded by the revision of the
// document it measured, so of two concurrent deletes only the one that
// removed the document releases it, and every delete it makes is guarded. A
// read that finds no document, or no active collection, is returned as the
// refusal it is; a caller's own IfRevision that no longer matches is the
// conflict the service would report.
func (s *quotaCollectionService) DeleteDocument(ctx context.Context, req *service.DeleteDocumentRequest) (*service.DeleteDocumentResponse, error) {
	conflict := &service.CollectionDocumentConflictError{Database: req.Database, Collection: req.Collection, ID: req.ID}
	for attempt := 0; attempt < guardedReplaceAttempts; attempt++ {
		current, err := s.readForGuard(ctx, req.Database, req.Collection, req.ID)
		if err != nil {
			return nil, err
		}
		if req.IfRevision != "" && req.IfRevision != current.Revision {
			return nil, conflict
		}
		guarded := *req
		guarded.IfRevision = current.Revision
		resp, derr := s.inner.DeleteDocument(ctx, &guarded)
		if isDocumentConflict(derr) && req.IfRevision == "" {
			continue
		}
		if derr != nil {
			return nil, derr
		}
		s.acct.Sub(s.tenantID, service.DocumentBodyBytes(current.Document))
		return resp, nil
	}
	return nil, conflict
}

// ListDocuments passes through.
func (s *quotaCollectionService) ListDocuments(ctx context.Context, req *service.ListDocumentsRequest) (*service.ListDocumentsResponse, error) {
	return s.inner.ListDocuments(ctx, req)
}

// SweepStuckCollections forwards to the wrapped service, which must be able
// to sweep: a collection surface that cannot finish its own interrupted
// operations says so rather than reporting an empty sweep. The body bytes the
// sweep's teardowns removed are released, also when the sweep reports
// failures alongside them.
func (s *quotaCollectionService) SweepStuckCollections(ctx context.Context, database string, staleAfter time.Duration) (service.CollectionSweepResult, error) {
	sweeper, ok := s.inner.(service.CollectionSweeper)
	if !ok {
		return service.CollectionSweepResult{}, notImplemented("collection sweep", "")
	}
	result, err := sweeper.SweepStuckCollections(ctx, database, staleAfter)
	if result.FreedBytes > 0 {
		s.acct.Sub(s.tenantID, result.FreedBytes)
	}
	return result, err
}

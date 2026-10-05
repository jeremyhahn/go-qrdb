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

package embedded

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

var _ transport.DatabaseScoper = (*Transport)(nil)

// WithServiceRegistry gives the transport the node's per-(tenant, database)
// service registry -- the one every network transport resolves requests
// through -- so WithDatabase can confine a scoped transport's data plane to a
// named database. Without it the transport operates on the node's default
// service set only, and WithDatabase refuses to scope.
func WithServiceRegistry(reg service.Registry) Option {
	return func(t *Transport) {
		t.registry = reg
	}
}

// databaseScope is the (registry, database) pair a scoped transport's
// data-plane services resolve through on every call.
type databaseScope struct {
	registry service.Registry
	database string
}

// tenant returns the tenant the call is made for: the context's, or the
// system tenant when none is bound, exactly as the unscoped transport's
// services read it.
func (s *databaseScope) tenant(ctx context.Context) string {
	return tenant.FromContext(ctx)
}

// errScopedNeedsContext is what a database-scoped adapter answers for a
// service method that carries no context (today, the time-series retention
// and metric-listing methods): resolving it for a guessed tenant would reach
// another tenant's database, so it fails closed. The transport never calls
// those methods on an adapter; it resolves them itself with the caller's
// context (Transport.timeSeriesFor).
var errScopedNeedsContext = &service.CapabilityUnsupportedError{
	Message: "embedded: a database-scoped call needs the caller's context to resolve its tenant",
}

//go:generate go run ./internal/scopegen -o database_scope_services.go -forward-o replica_forward_methods.go

// resolve returns the time-series service of the scoped database for ctx's
// tenant.
func (s *scopedTimeSeries) resolve(ctx context.Context) (service.TimeSeriesServicer, error) {
	return s.scope.registry.TimeSeriesService(s.scope.tenant(ctx), s.scope.database)
}

// timeSeriesFor returns the time-series service a call made with ctx uses:
// on a database-scoped transport the database's, resolved for the context's
// tenant, and otherwise the transport's own. It is for the service methods
// that take no context, which the scoped adapter cannot resolve itself.
func (t *Transport) timeSeriesFor(ctx context.Context) (service.TimeSeriesServicer, error) {
	if scoped, ok := t.tsSvc.(*scopedTimeSeries); ok {
		return scoped.resolve(ctx)
	}
	return t.tsSvc, nil
}

// WithDatabase implements transport.DatabaseScoper. It returns a transport
// whose data plane -- KV, MVCC, transactions, batches, watches, leases,
// indexes, time series, vectors, collections, locks and RAG -- is confined to
// database: each call resolves its service for (the context's tenant,
// database) through the node's service registry, as a network transport's
// server does per request, so a sealed or dropped database is refused rather
// than served. Everything else is shared with the receiver.
//
// Scoping to "" or "system" returns the receiver. Without a service registry
// (WithServiceRegistry) there is no way to reach another database, and the
// call fails with *transport.DatabaseScopeUnsupportedError rather than
// returning a transport that would serve the default database under the
// requested name.
func (t *Transport) WithDatabase(database string) (transport.Transport, error) {
	db := transport.NormalizeDatabaseScope(database)
	if db == "" {
		return t, nil
	}
	if t.registry == nil {
		return nil, &transport.DatabaseScopeUnsupportedError{
			Database: database,
			Reason:   "the embedded transport has no service registry (embedded.WithServiceRegistry)",
		}
	}
	scope := &databaseScope{registry: t.registry, database: db}
	c := t.cloneShared()
	c.kvSvc = scopedIfServed[service.KVServicer](t.kvSvc, &scopedKV{scope: scope})
	c.mvccSvc = scopedIfServed[service.MVCCKVServicer](t.mvccSvc, &scopedMVCC{scope: scope})
	c.txSvc = scopedIfServed[service.TransactionServicer](t.txSvc, &scopedTransaction{scope: scope})
	c.batchSvc = scopedIfServed[service.BatchServicer](t.batchSvc, &scopedBatch{scope: scope})
	c.watchSvc = scopedIfServed[service.WatchServicer](t.watchSvc, &scopedWatch{scope: scope})
	// The registry's watch service is already guarded by the seal stream
	// registry for the scoped (tenant, database); guarding again here would
	// subscribe every scoped watch twice.
	c.sealStreams = nil
	c.leaseSvc = scopedIfServed[service.LeaseServicer](t.leaseSvc, &scopedLease{scope: scope})
	c.indexSvc = scopedIfServed[service.IndexServicer](t.indexSvc, &scopedIndex{scope: scope})
	c.tsSvc = scopedIfServed[service.TimeSeriesServicer](t.tsSvc, &scopedTimeSeries{scope: scope})
	c.vectorSvc = scopedIfServed[service.VectorServicer](t.vectorSvc, &scopedVector{scope: scope})
	c.collectionSvc = scopedIfServed[service.CollectionServicer](t.collectionSvc, &scopedCollection{scope: scope})
	c.lockSvc = scopedIfServed[service.LockServicer](t.lockSvc, &scopedLock{scope: scope})
	if t.ragSvc != nil {
		c.ragSvc = &scopedRAG{inner: t.ragSvc, database: db}
	}
	if t.forwarding != nil {
		return &forwardingScope{Transport: c, database: db, fwd: t.forwarding}, nil
	}
	return c, nil
}

// scopedIfServed returns scoped when the receiver serves the family (served
// is non-nil) and nil otherwise. Scoping confines a family to a database; it
// never grants one the unscoped transport withholds -- a transport built
// without a family's servicer has a scoped clone that refuses it the same
// way instead of resolving it through the database registry.
func scopedIfServed[T any](served, scoped T) T {
	if any(served) == nil {
		var none T
		return none
	}
	return scoped
}

// cloneShared returns a new Transport sharing every service and setting of t.
// Its lazily built SQL service and its closed flag are its own; the closed
// flag starts as t's, like a network transport's scoped clone. Every field of
// Transport is listed here or in the sync-state list of
// TestCloneShared_CoversEveryField, so a field added to Transport fails that
// test until it is decided here.
func (t *Transport) cloneShared() *Transport {
	c := &Transport{
		kvSvc:             t.kvSvc,
		adminSvc:          t.adminSvc,
		mvccSvc:           t.mvccSvc,
		txSvc:             t.txSvc,
		batchSvc:          t.batchSvc,
		watchSvc:          t.watchSvc,
		leaseSvc:          t.leaseSvc,
		indexSvc:          t.indexSvc,
		tsSvc:             t.tsSvc,
		vectorSvc:         t.vectorSvc,
		userSvc:           t.userSvc,
		roleSvc:           t.roleSvc,
		certProv:          t.certProv,
		dbSvc:             t.dbSvc,
		barrierSvc:        t.barrierSvc,
		pinBackend:        t.pinBackend,
		dekSvc:            t.dekSvc,
		ceremonySvc:       t.ceremonySvc,
		ceremonyCA:        t.ceremonyCA,
		credentialSvc:     t.credentialSvc,
		backupSvc:         t.backupSvc,
		rebalanceSvc:      t.rebalanceSvc,
		embeddingSvc:      t.embeddingSvc,
		ragSvc:            t.ragSvc,
		collectionSvc:     t.collectionSvc,
		sealStoreSvc:      t.sealStoreSvc,
		tenantSvc:         t.tenantSvc,
		lockSvc:           t.lockSvc,
		authFactorSvc:     t.authFactorSvc,
		auditLogger:       t.auditLogger,
		encMigrator:       t.encMigrator,
		sealProbe:         t.sealProbe,
		sealStreams:       t.sealStreams,
		registry:          t.registry,
		rbacAuthz:         t.rbacAuthz,
		authMW:            t.authMW,
		userAuthFactorSvc: t.userAuthFactorSvc,
		loginResolver:     t.loginResolver,
		sessionIssuer:     t.sessionIssuer,
		identityHydrator:  t.identityHydrator,
		loginSessions:     t.loginSessions,
		poolManager:       t.poolManager,
		poolStateStore:    t.poolStateStore,
		forwarding:        t.forwarding,
		nowFunc:           t.nowFunc,
	}
	t.factorVerifierMu.RLock()
	if t.factorVerifiers != nil {
		c.factorVerifiers = make(map[transport.FactorType]FactorVerifier, len(t.factorVerifiers))
		for k, v := range t.factorVerifiers {
			c.factorVerifiers[k] = v
		}
	}
	t.factorVerifierMu.RUnlock()
	c.closed.Store(t.closed.Load())
	return c
}

// scopedRAG binds the scoped database to every RAG call. The RAG service is
// one per node and reads the (tenant, database) its records belong to from
// the context (service.ScopedRAGStore), so the scope travels there.
type scopedRAG struct {
	inner    service.RAGServicer
	database string
}

var _ service.RAGServicer = (*scopedRAG)(nil)

func (s *scopedRAG) bind(ctx context.Context) context.Context {
	return tenant.WithDatabase(ctx, s.database)
}

// IngestDocument implements service.RAGServicer.
func (s *scopedRAG) IngestDocument(ctx context.Context, req *service.IngestDocumentRequest) (*service.IngestDocumentResponse, error) {
	return s.inner.IngestDocument(s.bind(ctx), req)
}

// Query implements service.RAGServicer.
func (s *scopedRAG) Query(ctx context.Context, req *service.RAGQueryRequest) (*service.RAGQueryResponse, error) {
	return s.inner.Query(s.bind(ctx), req)
}

// DeleteDocument implements service.RAGServicer.
func (s *scopedRAG) DeleteDocument(ctx context.Context, req *service.DeleteRAGDocumentRequest) (*service.DeleteRAGDocumentResponse, error) {
	return s.inner.DeleteDocument(s.bind(ctx), req)
}

// ListDocuments implements service.RAGServicer.
func (s *scopedRAG) ListDocuments(ctx context.Context, req *service.ListRAGDocumentsRequest) (*service.ListRAGDocumentsResponse, error) {
	return s.inner.ListDocuments(s.bind(ctx), req)
}

// Close implements service.RAGServicer. The node owns the RAG service, so a
// scoped transport never closes it.
func (s *scopedRAG) Close() error { return nil }

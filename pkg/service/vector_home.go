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

package service

import (
	"context"
	"errors"
	"time"

	api "github.com/jeremyhahn/go-qrdb/pkg/api"
	"github.com/jeremyhahn/go-qrdb/pkg/interfaces"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// tenantVectorHomePrefix is the keyspace an isolated tenant's vectors occupy
// inside its home store.
const tenantVectorHomePrefix = "__vector/"

// TenantHomeResolver resolves a tenant's home store and reports whether the
// tenant is isolated (lives in a home) at all.
type TenantHomeResolver interface {
	tenant.StoreResolver
	tenant.HomeLocator
}

type tenantHomeResolverBox struct {
	resolver TenantHomeResolver
}

// tenantVectorHome is the cached vector view over one isolated tenant's home
// store.
type tenantVectorHome struct {
	kv kvstore.KVStore
	st *vector.KVVectorStorage
}

// SetTenantHomeResolver attaches the resolver that places an isolated
// tenant's vectors in its home store. A nil r detaches it, after which every
// non-system tenant is refused.
func (v *vectorService) SetTenantHomeResolver(r TenantHomeResolver) {
	v.homeMu.Lock()
	defer v.homeMu.Unlock()
	clear(v.homeStores)
	if r == nil {
		v.homes.Store(nil)
		return
	}
	v.homes.Store(&tenantHomeResolverBox{resolver: r})
}

// storageFor returns the vector store that holds tenantID's namespaces: the
// node-wide store for the system tenant and for tenants without a home, the
// tenant's home store under tenantVectorHomePrefix for an isolated tenant.
// A non-system tenant whose placement cannot be established is refused with
// *TenantVectorHomeError and never reaches the node-wide store.
func (v *vectorService) storageFor(ctx context.Context, tenantID string) (vector.Storage, error) {
	if tenantid.IsSystem(tenantID) {
		return v.storage, nil
	}
	box := v.homes.Load()
	if box == nil {
		return nil, &TenantVectorHomeError{TenantID: tenantID}
	}
	inHome, err := box.resolver.InHome(ctx, tenantID)
	if err != nil {
		return nil, &TenantVectorHomeError{TenantID: tenantID, Err: err}
	}
	if !inHome {
		return v.storage, nil
	}
	resolved, err := box.resolver.Resolve(ctx, tenantID)
	if err != nil {
		return nil, &TenantVectorHomeError{TenantID: tenantID, Err: err}
	}
	if resolved == nil {
		return nil, &TenantVectorHomeError{TenantID: tenantID, Err: vector.ErrInvalidConfig}
	}
	cacheable := kvstore.UsableAsMapKey(resolved)

	v.homeMu.Lock()
	defer v.homeMu.Unlock()
	if cached, ok := v.homeStores[tenantID]; ok && cacheable && cached.kv == resolved {
		return cached.st, nil
	}
	scoped, err := kvstore.NewNamespacedKVStore(resolved, tenantVectorHomePrefix)
	if err != nil {
		return nil, &TenantVectorHomeError{TenantID: tenantID, Err: err}
	}
	st, err := vector.NewKVVectorStorage(scoped)
	if err != nil {
		return nil, &TenantVectorHomeError{TenantID: tenantID, Err: err}
	}
	if cacheable {
		v.homeStores[tenantID] = &tenantVectorHome{kv: resolved, st: st}
	} else {
		delete(v.homeStores, tenantID)
	}
	v.seedHomeMetrics(ctx, tenantID, st)
	return st, nil
}

// seedHomeMetrics seeds, once per tenant, the vector metrics from the
// namespaces a tenant's home store already holds: loadNamespaces reads only
// the node-local store, so after a restart a home-backed namespace would
// otherwise count from zero. It runs under homeMu, on the tenant's first
// home access, before any write of this process reaches that store.
func (v *vectorService) seedHomeMetrics(ctx context.Context, tenantID string, st vector.Storage) {
	if v.metrics == nil || v.homeSeeded[tenantID] {
		return
	}
	namespaces, err := st.ListNamespaces(ctx)
	if err != nil {
		// Unseeded: the next home access retries.
		return
	}
	v.homeSeeded[tenantID] = true
	for i := range namespaces {
		v.seedNamespaceMetrics(ctx, st, &namespaces[i])
	}
}

// storageForNamespace returns the store of the tenant a scoped namespace
// belongs to.
func (v *vectorService) storageForNamespace(ctx context.Context, scoped string) (vector.Storage, error) {
	tenantID, _ := unscopedNamespace(scoped)
	return v.storageFor(ctx, tenantID)
}

// generationStorage is a vector store that counts each namespace's writes,
// so a node can tell its cached index from one a write elsewhere outdated.
type generationStorage interface {
	vector.Storage
	Version(ctx context.Context, namespace string) (vector.NamespaceVersion, error)
}

// homeIndexGeneration is the home-store write generation a cached index
// reflects.
type homeIndexGeneration struct {
	index interfaces.HNSWIndex
	ver   vector.NamespaceVersion
}

// homeIndex returns scoped's index for a tenant whose vectors live in home,
// rebuilt from home when a write has advanced the namespace's generation past
// the cached one, on this node or another replica of the home.
func (v *vectorService) homeIndex(ctx context.Context, home generationStorage, scoped string) (interfaces.HNSWIndex, error) {
	ver, err := home.Version(ctx, scoped)
	if err != nil {
		return nil, homeReadError(scoped, err)
	}
	v.indexMu.RLock()
	index, cached := v.hnswIndexes[scoped]
	seen, known := v.homeGens[scoped]
	v.indexMu.RUnlock()
	// Reuse only an index built from exactly this incarnation and generation:
	// a delete+recreate changes the incarnation even when the generation
	// count happens to line up.
	if cached && known && seen.index == index && seen.ver == ver {
		return index, nil
	}
	built, _, err := v.buildHNSWIndex(ctx, home, scoped, true)
	if err != nil {
		return nil, homeReadError(scoped, err)
	}
	v.indexMu.Lock()
	if cur, ok := v.hnswIndexes[scoped]; ok {
		if seen, known := v.homeGens[scoped]; known && seen.index == cur &&
			seen.ver.Incarnation == ver.Incarnation && seen.ver.Generation >= ver.Generation {
			v.indexMu.Unlock()
			return cur, nil
		}
	}
	if v.homeGens == nil {
		v.homeGens = make(map[string]homeIndexGeneration)
	}
	v.hnswIndexes[scoped] = built
	v.homeGens[scoped] = homeIndexGeneration{index: built, ver: ver}
	v.indexMu.Unlock()
	// The rebuilt index replaces the one the index-size gauge measured.
	v.refreshIndexSizeMetric(scoped)
	return built, nil
}

// homeReadError turns a failure to read scoped's version or vectors from its
// tenant's home store into a TenantVectorHomeError, so a search reports it
// instead of answering with no results. A namespace the home does not hold,
// a stored geometry the service refuses and an index it cannot construct
// are not read failures and keep their own errors.
func homeReadError(scoped string, err error) error {
	if errors.Is(err, vector.ErrNamespaceNotFound) {
		return err
	}
	if _, ok := errors.AsType[*UnsupportedNamespaceGeometryError](err); ok {
		return err
	}
	if _, ok := errors.AsType[*api.OperationFailedError](err); ok {
		return err
	}
	tenantID, _ := unscopedNamespace(scoped)
	return &TenantVectorHomeError{TenantID: tenantID, Err: err}
}

// noteHomeWrite moves the cached index's generation past this node's own
// write, which the caller applies to that index next, when no other write
// came between; otherwise the next search rebuilds the index.
func (v *vectorService) noteHomeWrite(ctx context.Context, st vector.Storage, scoped string) {
	home, ok := st.(generationStorage)
	if !ok || st == v.storage {
		return
	}
	ver, err := home.Version(ctx, scoped)
	if err != nil {
		return
	}
	v.indexMu.Lock()
	defer v.indexMu.Unlock()
	seen, known := v.homeGens[scoped]
	// Only this node's own write, one step past the cached version within the
	// same incarnation, moves the cached version forward.
	if known && seen.index == v.hnswIndexes[scoped] &&
		seen.ver.Incarnation == ver.Incarnation && seen.ver.Generation+1 == ver.Generation {
		seen.ver = ver
		v.homeGens[scoped] = seen
	}
}

// tenantRoutedVectorStorage is the vector.Storage the service's request paths
// use: each call goes to the store of the namespace's tenant.
type tenantRoutedVectorStorage struct {
	v *vectorService
}

var _ vector.Storage = (*tenantRoutedVectorStorage)(nil)

func (r *tenantRoutedVectorStorage) UpsertVector(ctx context.Context, namespace string, vec *vector.VectorValue) error {
	st, err := r.v.storageForNamespace(ctx, namespace)
	if err != nil {
		return err
	}
	if err := st.UpsertVector(ctx, namespace, vec); err != nil {
		return err
	}
	r.v.noteHomeWrite(ctx, st, namespace)
	return nil
}

func (r *tenantRoutedVectorStorage) GetVector(ctx context.Context, namespace string, id uint64) (*vector.VectorValue, error) {
	st, err := r.v.storageForNamespace(ctx, namespace)
	if err != nil {
		return nil, err
	}
	return st.GetVector(ctx, namespace, id)
}

func (r *tenantRoutedVectorStorage) DeleteVector(ctx context.Context, namespace string, id uint64) error {
	st, err := r.v.storageForNamespace(ctx, namespace)
	if err != nil {
		return err
	}
	if err := st.DeleteVector(ctx, namespace, id); err != nil {
		return err
	}
	r.v.noteHomeWrite(ctx, st, namespace)
	return nil
}

func (r *tenantRoutedVectorStorage) BatchUpsertVectors(ctx context.Context, namespace string, vecs []*vector.VectorValue) error {
	st, err := r.v.storageForNamespace(ctx, namespace)
	if err != nil {
		return err
	}
	if err := st.BatchUpsertVectors(ctx, namespace, vecs); err != nil {
		return err
	}
	r.v.noteHomeWrite(ctx, st, namespace)
	return nil
}

func (r *tenantRoutedVectorStorage) BatchDeleteVectors(ctx context.Context, namespace string, ids []uint64) error {
	st, err := r.v.storageForNamespace(ctx, namespace)
	if err != nil {
		return err
	}
	return st.BatchDeleteVectors(ctx, namespace, ids)
}

func (r *tenantRoutedVectorStorage) ScanByMetadata(ctx context.Context, namespace, field, value string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
	st, err := r.v.storageForNamespace(ctx, namespace)
	if err != nil {
		return nil, err
	}
	return st.ScanByMetadata(ctx, namespace, field, value, opts)
}

func (r *tenantRoutedVectorStorage) ScanByMultipleFilters(ctx context.Context, namespace string, filters []vector.MetadataFilter, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
	st, err := r.v.storageForNamespace(ctx, namespace)
	if err != nil {
		return nil, err
	}
	return st.ScanByMultipleFilters(ctx, namespace, filters, opts)
}

func (r *tenantRoutedVectorStorage) ScanNamespace(ctx context.Context, namespace string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
	st, err := r.v.storageForNamespace(ctx, namespace)
	if err != nil {
		return nil, err
	}
	return st.ScanNamespace(ctx, namespace, opts)
}

func (r *tenantRoutedVectorStorage) CreateNamespace(ctx context.Context, info *vector.NamespaceInfo) error {
	if info == nil {
		return vector.ErrInvalidNamespace
	}
	st, err := r.v.storageForNamespace(ctx, info.Name)
	if err != nil {
		return err
	}
	return st.CreateNamespace(ctx, info)
}

func (r *tenantRoutedVectorStorage) UpdateNamespace(ctx context.Context, namespace string, update *vector.NamespaceUpdate) error {
	st, err := r.v.storageForNamespace(ctx, namespace)
	if err != nil {
		return err
	}
	return st.UpdateNamespace(ctx, namespace, update)
}

func (r *tenantRoutedVectorStorage) DeleteNamespace(ctx context.Context, namespace string) error {
	st, err := r.v.storageForNamespace(ctx, namespace)
	if err != nil {
		return err
	}
	return st.DeleteNamespace(ctx, namespace)
}

// ListNamespaces lists the namespaces of the store the caller's tenant uses.
func (r *tenantRoutedVectorStorage) ListNamespaces(ctx context.Context) ([]vector.NamespaceInfo, error) {
	st, err := r.v.storageFor(ctx, tenant.FromContext(ctx))
	if err != nil {
		return nil, err
	}
	return st.ListNamespaces(ctx)
}

func (r *tenantRoutedVectorStorage) GetNamespaceInfo(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
	st, err := r.v.storageForNamespace(ctx, namespace)
	if err != nil {
		return nil, err
	}
	return st.GetNamespaceInfo(ctx, namespace)
}

func (r *tenantRoutedVectorStorage) GetStats(ctx context.Context, namespace string) (*vector.NamespaceStats, error) {
	st, err := r.v.storageForNamespace(ctx, namespace)
	if err != nil {
		return nil, err
	}
	return st.GetStats(ctx, namespace)
}

func (r *tenantRoutedVectorStorage) NextID(namespace string) (uint64, error) {
	st, err := r.v.storageForNamespace(context.Background(), namespace)
	if err != nil {
		return 0, err
	}
	return st.NextID(namespace)
}

func (r *tenantRoutedVectorStorage) BatchAllocateIDs(namespace string, count int) ([]uint64, error) {
	st, err := r.v.storageForNamespace(context.Background(), namespace)
	if err != nil {
		return nil, err
	}
	return st.BatchAllocateIDs(namespace, count)
}

func (r *tenantRoutedVectorStorage) Compact(ctx context.Context, namespace string) error {
	st, err := r.v.storageForNamespace(ctx, namespace)
	if err != nil {
		return err
	}
	return st.Compact(ctx, namespace)
}

// CleanupTombstones applies to the node-wide store; a home store has none.
func (r *tenantRoutedVectorStorage) CleanupTombstones(ctx context.Context, olderThan time.Duration) error {
	return r.v.storage.CleanupTombstones(ctx, olderThan)
}

// Close closes nothing: the service owns the node-wide store and a home store
// belongs to its tenant.
func (r *tenantRoutedVectorStorage) Close() error { return nil }

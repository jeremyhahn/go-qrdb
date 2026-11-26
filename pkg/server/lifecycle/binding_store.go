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

package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// binding_store.go materializes a database's local binding (docs/databases/
// engineering.md "Server-side binding"): a database that resolves to a
// local binding gets its own store, engine and service host under the
// binding's data root instead of a key prefix in the node's shared store.
//
// On a standalone node it materializes the database of a prefix- or
// barrier-isolated tenant and of the system tenant (except its system
// database, which stays on the control-plane store), at the default root or
// at an explicit data_dir, as long as the database has no per-database
// barrier. A prefix tenant's and the system tenant's store is written
// through the node encryptor; a barrier tenant's through the tenant's own
// barrier (WithTenantEncryptors), never the node encryptor, so sealing that
// barrier fails the database's data closed. A database with a per-database
// barrier, a raft-mode node, and volume and raft tenants are delegated
// unchanged to database.NewEmbeddedBackendFactory (docs/plans/923.md).
// An objstore database on the local provider lives under its binding root;
// storage.objstore's path places the node's own store only, and a remote
// provider is unchanged.

// LocalBindingFactory is the database.BackendFactory of a standalone node
// that opens a qualifying database's own store under its binding root (an
// explicit data_dir when one is set), encrypted by the binding's encryptor
// scope: the node encryptor for a prefix tenant and the system tenant, the
// tenant's barrier for a barrier tenant, the tenant's encryptor for a volume
// tenant, whose memory and pebble databases live under the tenant's data
// root.
type LocalBindingFactory struct {
	node       database.BindingNode
	enc        *atrest.Encryptor
	tenantEnc  func(tenantID string) (*atrest.Encryptor, error)
	storageCfg qrdbconfig.StorageConfig
	tenants    func(tenantID string) (database.BindingTenant, error)
	fallback   database.BackendFactory
	hubOpts    []watch.HubOption
	// policy is the live cluster isolation policy each materialized
	// database's vector service consults, as the shared vector service
	// does; nil means no requirement.
	policy tenant.ClusterIsolationPolicyReader
}

// NewLocalBindingFactory returns a factory for node. enc is the node
// encryptor every on-disk binding store writes through (nil or disabled
// leaves them plaintext); storageCfg is the node's storage configuration
// (NewLocalStorage); an objstore database on the local provider lives under
// its binding root, storage.objstore's path places the node's own store
// only, and a remote provider is unchanged. tenants looks up the owning tenant of a database; a nil
// tenants treats every tenant as prefix-isolated. The system tenant is never
// looked up: its databases (except the system database) and a database at
// an explicit data_dir without a per-database barrier are materialized; a
// per-database barrier, raft mode, and raft tenants stay delegated to the
// embedded factory. A volume tenant's database is materialized under the
// tenant's data root once WithTenantEncryptors is set; its name-visible
// backends are refused. hubOpts configure the watch
// hub of each materialized database.
func NewLocalBindingFactory(node database.BindingNode, enc *atrest.Encryptor, storageCfg qrdbconfig.StorageConfig, tenants func(string) (database.BindingTenant, error), hubOpts ...watch.HubOption) *LocalBindingFactory {
	return &LocalBindingFactory{
		node:       node,
		enc:        enc,
		storageCfg: storageCfg,
		tenants:    tenants,
		fallback:   database.NewEmbeddedBackendFactory(),
		hubOpts:    hubOpts,
	}
}

// WithTenantEncryptors sets lookup, the encryptor of a barrier tenant's
// binding: the tenant's own barrier, looked up per database at open. A
// lookup error fails the open closed (a *BindingOpenError wrapping it). nil,
// the default, keeps barrier tenants delegated to the embedded factory,
// unchanged. It returns f.
func (f *LocalBindingFactory) WithTenantEncryptors(lookup func(string) (*atrest.Encryptor, error)) *LocalBindingFactory {
	f.tenantEnc = lookup
	return f
}

// WithIsolationPolicy wires the cluster isolation policy reader every
// materialized database's vector service consults, so a strict policy
// refuses a system-tenant namespace create in a named database exactly as
// the shared vector service does. It returns f.
func (f *LocalBindingFactory) WithIsolationPolicy(r tenant.ClusterIsolationPolicyReader) *LocalBindingFactory {
	f.policy = r
	return f
}

// WithRaftMode marks the factory's node as a raft-mode node: a backend: raft
// database is then left to the Manager's raft path (nil, nil) instead of
// refused, and every other local kind is materialized as on a standalone node.
func (f *LocalBindingFactory) WithRaftMode() *LocalBindingFactory {
	f.node.RaftMode = true
	return f
}

// ReservedRoots reports the paths the node's own stores occupy as storage
// actually opens them, so the database Manager refuses a data directory
// overlapping any of them and never removes one
// (database.ReservedRootsReporter): storage.wal_dir whenever it is set, made
// absolute against the working directory as Pebble opens a relative one; and,
// for the local object-store provider, the settings path resolved exactly as
// NewLocalStorage resolves it (empty: <data dir>/objstore; relative: against
// the working directory, as the local provider opens it), its physical
// directory and its commit-journal sibling (objstore.LocalRoots). A path that
// cannot be resolved is an error: the Manager then fails closed.
func (f *LocalBindingFactory) ReservedRoots() ([]string, error) {
	var roots []string
	if wal := f.storageCfg.WALDir; wal != "" {
		abs, err := filepath.Abs(wal)
		if err != nil {
			return nil, fmt.Errorf("resolve storage.wal_dir %q: %w", wal, err)
		}
		roots = append(roots, abs)
	}
	provider, settings := resolveObjstoreSettings(f.storageCfg.Objstore, f.node.DataDir)
	if provider == "local" {
		local, err := objstore.LocalRoots(settings["path"])
		if err != nil {
			return nil, fmt.Errorf("resolve storage.objstore.settings.path %q: %w", settings["path"], err)
		}
		roots = append(roots, local...)
	}
	return roots, nil
}

// lateIsolationPolicy reads the cluster isolation policy from the tenant
// service each call, because the service exists only once the node's
// services are built, after the manager that opens databases.
type lateIsolationPolicy struct {
	tenants func() tenant.TenantServicer
}

// ClusterIsolationPolicy returns the tenant service's live policy. Before
// the service exists it fails closed with IsolationPolicyUnavailableError;
// a service that keeps no policy imposes no requirement, matching the
// shared vector service, which is wired only to a reader.
func (l lateIsolationPolicy) ClusterIsolationPolicy(ctx context.Context) (tenant.ClusterIsolationPolicy, error) {
	var svc tenant.TenantServicer
	if l.tenants != nil {
		svc = l.tenants()
	}
	if svc == nil {
		return tenant.ClusterIsolationPolicyUnset, &IsolationPolicyUnavailableError{}
	}
	r, ok := svc.(tenant.ClusterIsolationPolicyReader)
	if !ok {
		return tenant.ClusterIsolationPolicyUnset, nil
	}
	return r.ClusterIsolationPolicy(ctx)
}

// materializes reports whether a tenant of isolation gets its databases
// materialized: a prefix tenant (or an unset isolation) always, a barrier
// tenant only with a tenant encryptor lookup.
func (f *LocalBindingFactory) materializes(isolation tenant.IsolationType) bool {
	switch isolation {
	case "", tenant.IsolationPrefix:
		return true
	case tenant.IsolationBarrier, tenant.IsolationVolume:
		return f.tenantEnc != nil
	default:
		return false
	}
}

// Create opens cfg's backend. A qualifying database gets a
// *MaterializedBinding over its own store; any other is opened by the
// embedded factory at dataDir, unchanged.
func (f *LocalBindingFactory) Create(cfg database.DatabaseConfig, dataDir string) (database.Backend, error) {
	b, err := f.resolveLocal(&cfg)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return f.fallback.Create(cfg, dataDir)
	}
	return f.open(b)
}

// RecordRoot is the store root Create opens for the database cfg describes,
// without opening it (database.RecordRootResolver): the materialized
// binding's root, or "" for a database served from the fallback store,
// which owns no root of its own. The manager checks it for every catalog
// record, opened on this node or not, before a create claims a root or a
// drop removes one.
func (f *LocalBindingFactory) RecordRoot(cfg database.DatabaseConfig) (string, error) {
	if cfg.Backend == database.BackendRaft {
		if !f.node.RaftMode || !f.ownsRoot(&cfg) {
			return "", nil
		}
		// A raft database's group store lives at the root its binding resolves to.
		b, err := f.DescribeBinding(cfg)
		var unresolved *database.BindingRootUnresolvedError
		if errors.As(err, &unresolved) {
			// A tenant with no data root on this node holds no group root here.
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return b.DataRoot, nil
	}
	b, err := f.resolveLocal(&cfg)
	if err != nil || b == nil {
		return "", err
	}
	return b.DataRoot, nil
}

// RecordBinding is the local binding Create materializes for the database
// cfg describes, without opening it (database.RecordBindingResolver), or
// nil for a database served from the fallback store (on a raft-mode node, a
// backend: raft database, which the Manager serves on its raft path). A node-wide
// operation over every database's own store (an encryption migration)
// reads it for every catalog record, opened on this node or not.
func (f *LocalBindingFactory) RecordBinding(cfg database.DatabaseConfig) (*database.Binding, error) {
	if !f.node.RaftMode && cfg.Backend == database.BackendRaft {
		return nil, nil
	}
	return f.resolveLocal(&cfg)
}

// DescribeBinding is the binding ResolveBinding decides for the database
// cfg describes, opening nothing. Unlike RecordBinding it describes a
// database the factory serves from the fallback store too: database info
// reports the binding of every database, materialized here or not. A
// backend: raft record on a standalone node is refused as Create refuses it.
func (f *LocalBindingFactory) DescribeBinding(cfg database.DatabaseConfig) (*database.Binding, error) {
	if !f.node.RaftMode && cfg.Backend == database.BackendRaft {
		return nil, &database.RaftBackendUnavailableError{Name: cfg.Name}
	}
	t, err := f.tenant(cfg.TenantID)
	if err != nil {
		return nil, &BindingOpenError{TenantID: cfg.TenantID, Database: cfg.Name, Backend: cfg.Backend, Err: err}
	}
	return database.ResolveBinding(&cfg, t, f.node)
}

var _ database.BindingDescriber = (*LocalBindingFactory)(nil)

// resolveLocal is the local binding Create materializes for cfg, or nil when
// cfg's database is served from the fallback store.
func (f *LocalBindingFactory) resolveLocal(cfg *database.DatabaseConfig) (*database.Binding, error) {
	// A standalone node has no Raft host: a backend: raft record is
	// refused, on create and when a recorded database is provisioned, the
	// way ResolveBinding refuses it, never served from an unreplicated
	// local store.
	if !f.node.RaftMode && cfg.Backend == database.BackendRaft {
		return nil, &database.RaftBackendUnavailableError{Name: cfg.Name}
	}
	if !f.mayQualify(cfg) {
		return nil, nil
	}
	t, err := f.tenant(cfg.TenantID)
	if err != nil {
		return nil, &BindingOpenError{TenantID: cfg.TenantID, Database: cfg.Name, Backend: cfg.Backend, Err: err}
	}
	if !f.materializes(t.Isolation) {
		return nil, nil
	}
	b, err := database.ResolveBinding(cfg, t, f.node)
	if err != nil {
		return nil, err
	}
	if b.Kind != database.BindingLocal {
		return nil, nil
	}
	if b.Backend == database.BackendObjstore {
		if p := f.storageCfg.Objstore.Provider; p != "" && p != "local" {
			return nil, &RemoteObjstoreBindingUnsupportedError{TenantID: b.TenantID, Database: b.Database, Provider: p}
		}
	}
	return b, nil
}

// Close releases the embedded factory.
func (f *LocalBindingFactory) Close() error {
	return f.fallback.Close()
}

// mayQualify reports the checks that need no tenant lookup. They are the same
// on a standalone and a raft-mode node: a raft-mode node materializes a
// memory, file, pebble or objstore database the way a standalone node does.
func (f *LocalBindingFactory) mayQualify(cfg *database.DatabaseConfig) bool {
	if !f.ownsRoot(cfg) {
		return false
	}
	// A backend: raft database is not this factory's on a raft-mode node:
	// the Manager keeps its raft path for it (standalone nodes refuse it
	// before this check).
	return cfg.Backend != database.BackendRaft
}

// ownsRoot reports whether cfg's database has a binding root of its own: the
// system database and tenant homes live on the control-plane store.
func (f *LocalBindingFactory) ownsRoot(cfg *database.DatabaseConfig) bool {
	if tenantid.IsSystem(cfg.TenantID) && cfg.Name == SystemDatabase {
		return false
	}
	return !database.IsTenantHome(cfg.Name)
}

func (f *LocalBindingFactory) tenant(tenantID string) (database.BindingTenant, error) {
	// The system tenant is prefix-isolated by construction (the tenant
	// service refuses any other isolation for it), so it is never looked up.
	if tenantid.IsSystem(tenantID) {
		return database.BindingTenant{ID: tenantid.System, Isolation: tenant.IsolationPrefix}, nil
	}
	if f.tenants == nil {
		return database.BindingTenant{ID: tenantID}, nil
	}
	t, err := f.tenants(tenantID)
	if err != nil {
		return database.BindingTenant{}, err
	}
	if t.ID == "" {
		t.ID = tenantID
	}
	return t, nil
}

// TenantServiceBindingLookup returns the tenant lookup NewLocalBindingFactory
// takes, reading each tenant's isolation from the tenant service get returns.
// get is late-bound: the node builds the factory before its tenant service
// exists, and the lookup fails closed with a *BindingTenantUnavailableError
// while get returns nil, never treating an unknown tenant as prefix. A
// GetTenant error is returned as is (the factory wraps it in a
// BindingOpenError). A volume tenant's DataRoot is its local data root from
// the first resolver roots returns; a missing resolver or an unresolvable
// root fails closed with a *BindingTenantUnavailableError, so a volume
// tenant's database never opens under the node's root. DataRoot stays empty
// for every other isolation.
func TenantServiceBindingLookup(get func() tenant.TenantServicer, roots ...func() TenantDataRootResolver) func(string) (database.BindingTenant, error) {
	return func(tenantID string) (database.BindingTenant, error) {
		var svc tenant.TenantServicer
		if get != nil {
			svc = get()
		}
		if svc == nil {
			return database.BindingTenant{}, &BindingTenantUnavailableError{TenantID: tenantID}
		}
		info, err := svc.GetTenant(context.Background(), tenantID)
		if err != nil {
			return database.BindingTenant{}, err
		}
		if info == nil {
			return database.BindingTenant{}, &BindingTenantUnavailableError{TenantID: tenantID}
		}
		bt := database.BindingTenant{ID: tenantID, Isolation: info.EffectiveIsolationType()}
		if bt.Isolation == tenant.IsolationVolume {
			// A volume tenant's databases get their own stores under the
			// tenant's data root on this node. A root that cannot be
			// resolved fails closed: the database is never bound as if its
			// tenant were prefix-isolated.
			root, err := volumeDataRoot(tenantID, roots)
			if err != nil {
				return database.BindingTenant{}, err
			}
			bt.DataRoot = root
		}
		return bt, nil
	}
}

// registryDataRoots adapts the late-bound barrier registry to the data root
// resolver TenantServiceBindingLookup takes; a nil registry is no resolver.
func registryDataRoots(get func() *seal.BarrierRegistry) func() TenantDataRootResolver {
	return func() TenantDataRootResolver {
		if get == nil {
			return nil
		}
		if reg := get(); reg != nil {
			return reg
		}
		return nil
	}
}

// TenantDataRootResolver resolves a volume-isolated tenant's data root on
// this node (*seal.BarrierRegistry.LocalTenantDataRoot).
type TenantDataRootResolver interface {
	LocalTenantDataRoot(ctx context.Context, tenantID string) (string, error)
}

// volumeDataRoot resolves tenantID's data root through each resolver of
// roots in order; the first root resolved wins. When no resolver yields a
// root (none present, an error or an empty root) it is a
// *BindingTenantUnavailableError.
func volumeDataRoot(tenantID string, roots []func() TenantDataRootResolver) (string, error) {
	for _, get := range roots {
		if get == nil {
			continue
		}
		r := get()
		if r == nil {
			continue
		}
		if root, err := r.LocalTenantDataRoot(context.Background(), tenantID); err == nil && root != "" {
			return root, nil
		}
	}
	return "", &BindingTenantUnavailableError{TenantID: tenantID}
}

// TenantBarrierEncryptorLookup returns the tenant encryptor lookup
// WithTenantEncryptors takes: an encryptor over the tenant's own barrier in
// the registry get returns. get is late-bound like TenantServiceBindingLookup:
// while it is nil or returns nil the lookup fails closed with a
// *BindingBarrierUnavailableError. A GetTenant error (seal.ErrTenantNotFound)
// is returned as is, and a sealed tenant barrier gives a
// *BindingBarrierSealedError; the node encryptor is never a stand-in.
func TenantBarrierEncryptorLookup(get func() *seal.BarrierRegistry) func(string) (*atrest.Encryptor, error) {
	return func(tenantID string) (*atrest.Encryptor, error) {
		var reg *seal.BarrierRegistry
		if get != nil {
			reg = get()
		}
		if reg == nil {
			return nil, &BindingBarrierUnavailableError{TenantID: tenantID}
		}
		tb, err := reg.GetTenant(tenantID)
		if err != nil {
			return nil, err
		}
		if tb.IsSealed() {
			return nil, &BindingBarrierSealedError{TenantID: tenantID}
		}
		return atrest.New(tb.Barrier()), nil
	}
}

// NewNodeBindingFactory is the one binding factory a standalone node -- qrdbd
// and the embedded sdk/go/node standalone node alike -- installs on its
// database manager: bindings rooted under dataDir with the node's
// database_defaults, sealed under enc, with a database's tenant and its
// tenant barrier resolved live through tenants and barriers. Both lookups are
// late-bound because the tenant service and the barrier registry exist only
// once the services are built, after the manager.
func NewNodeBindingFactory(dataDir string, defaults database.Defaults, enc *atrest.Encryptor, storageCfg qrdbconfig.StorageConfig, tenants func() tenant.TenantServicer, barriers func() *seal.BarrierRegistry, hubOpts ...watch.HubOption) *LocalBindingFactory {
	return NewLocalBindingFactory(database.BindingNode{DataDir: dataDir, Defaults: defaults}, enc, storageCfg,
		TenantServiceBindingLookup(tenants, registryDataRoots(barriers)), hubOpts...).
		WithTenantEncryptors(TenantBarrierEncryptorLookup(barriers)).
		WithIsolationPolicy(lateIsolationPolicy{tenants: tenants})
}

// encryptor returns the encryptor b's store is written through, by its
// encryptor scope: the node encryptor for EncryptorNode, the tenant's barrier
// for EncryptorTenant. A tenant-scoped binding never falls back to the node
// encryptor.
func (f *LocalBindingFactory) encryptor(b *database.Binding) (*atrest.Encryptor, error) {
	switch b.Encryptor {
	case database.EncryptorNode:
		return f.enc, nil
	case database.EncryptorTenant:
		if f.tenantEnc == nil {
			return nil, &BindingBarrierUnavailableError{TenantID: b.TenantID}
		}
		return f.tenantEnc(b.TenantID)
	default:
		return nil, errors.New("lifecycle: binding encryptor scope " + string(b.Encryptor) + " not supported")
	}
}

func (f *LocalBindingFactory) open(b *database.Binding) (*MaterializedBinding, error) {
	openErr := func(err error) error {
		return &BindingOpenError{TenantID: b.TenantID, Database: b.Database, Backend: b.Backend, Err: err}
	}
	enc, err := f.encryptor(b)
	if err != nil {
		return nil, openErr(err)
	}
	if b.Backend == database.BackendObjstore && enc.Encrypts() {
		return nil, &ObjstoreBindingEncryptionUnsupportedError{TenantID: b.TenantID, Database: b.Database}
	}
	storageCfg := bindingStorageConfig(f.storageCfg, b)
	if b.Backend == database.BackendPebble {
		if err := clearStaleBindingWAL(storageCfg.WALDir, b.DataRoot); err != nil {
			return nil, openErr(err)
		}
	}
	stg, pdb, err := NewLocalStorage(b.Backend, b.DataRoot, enc, storageCfg)
	if err != nil {
		return nil, openErr(err)
	}
	if b.Backend == database.BackendMemory && b.Encryptor == database.EncryptorTenant {
		tenantID := b.TenantID
		stg = &sealGatedStorage{Storage: stg, check: func() error {
			_, err := f.tenantEnc(tenantID)
			if _, sealed := errors.AsType[*BindingBarrierSealedError](err); sealed {
				// database.SealedError is the refusal every service
				// translates to its sealed status, as for a sealed
				// encrypted store.
				return errors.Join(&database.SealedError{}, err)
			}
			return err
		}}
	}
	m := &MaterializedBinding{binding: b, hub: watch.NewHub(f.hubOpts...)}
	if b.Backend == database.BackendPebble {
		m.walDir = storageCfg.WALDir
	}
	if b.DatabaseBarrier {
		// The database's barrier is layered on the binding's own store,
		// under the full bundle; the Manager attaches it (LayerBarrier).
		m.barrier = &databaseBarrierStorage{Storage: stg}
		stg = m.barrier
	}
	m.stg = stg
	m.StandaloneBackend = database.NewStandaloneBackend(stg)
	m.build = func() error {
		lr := lease.NewLessor()
		eng, err := NewLocalEngine(b.Backend, stg, pdb, m.hub, lr)
		if err != nil {
			return openErr(err)
		}
		local := backend.NewLocalBackend(eng)
		vec, closeVec, err := newBindingVector(local, f.policy)
		if err != nil {
			return openErr(errors.Join(err, local.Close()))
		}
		m.local, m.vector, m.closeVector = local, vec, closeVec
		m.stop = StartLocalLeaseExpiry(context.Background(), lr, service.NewLeaseService(local, shard.ShardKV), slog.Default())
		return nil
	}
	// A barrier binding opens its engine once its barrier is attached and
	// unsealed (Ready): the engine reads the store at open.
	if m.barrier == nil {
		if err := m.Ready(); err != nil {
			m.hub.Close()
			return nil, errors.Join(err, stg.Close())
		}
	}
	return m, nil
}

// newBindingVector builds the binding's own vector service with the
// constructor the node's vector service uses (service.NewVectorService, the
// qrdb_vector build-tag seam). It has no storage of its own: through
// service.WithVectorApplyHost every write is an OpVector* proposal into the
// binding's local backend and every read an engine vector lookup, so the
// vectors live in the binding store under the binding's encryptor, whatever
// its backend. There is no per-database <root>/vector store. The feature
// gates come from the host. The returned close stops the service.
func newBindingVector(local *backend.LocalBackend, policy tenant.ClusterIsolationPolicyReader) (service.VectorServicer, func() error, error) {
	log := logger.NewAdapter(newMinimalLogger(slog.Default()))
	opts := []service.VectorServiceOption{service.WithVectorApplyHost(local, shard.ShardKV)}
	if policy != nil {
		opts = append(opts, service.WithClusterIsolationPolicyReader(policy))
	}
	svc, err := service.NewVectorService(nil, log, opts...)
	if err != nil {
		return nil, nil, &VectorServiceError{Err: err}
	}
	closeFn := func() error {
		if c, ok := svc.(io.Closer); ok {
			return c.Close()
		}
		return nil
	}
	return svc, closeFn, nil
}

// sealGatedStorage is a memory store of a tenant-barrier binding gated on
// the barrier: a memory store holds plaintext, so no decrypt fails once the
// tenant seals, and the gate refuses every read and write instead, through
// the store, its batches, snapshots and iterators, with the barrier lookup's
// error (a *database.SealedError joined with *BindingBarrierSealedError when
// sealed). A handle resolved before
// the seal therefore stops serving at the seal, as an encrypted store does.
type sealGatedStorage struct {
	storage.Storage
	check func() error
}

func (s *sealGatedStorage) Get(key []byte) ([]byte, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	return s.Storage.Get(key)
}

func (s *sealGatedStorage) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	return gatedIterator(s.Storage.NewIterator(opts), s.check)
}

func (s *sealGatedStorage) Set(key, value []byte) error {
	if err := s.check(); err != nil {
		return err
	}
	return s.Storage.Set(key, value)
}

func (s *sealGatedStorage) Delete(key []byte) error {
	if err := s.check(); err != nil {
		return err
	}
	return s.Storage.Delete(key)
}

func (s *sealGatedStorage) DeleteRange(start, end []byte) error {
	if err := s.check(); err != nil {
		return err
	}
	return s.Storage.DeleteRange(start, end)
}

func (s *sealGatedStorage) List(prefix []byte) ([][]byte, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	return s.Storage.List(prefix)
}

func (s *sealGatedStorage) Exists(key []byte) (bool, error) {
	if err := s.check(); err != nil {
		return false, err
	}
	return s.Storage.Exists(key)
}

func (s *sealGatedStorage) Batch() storage.Batch {
	return &sealGatedBatch{Batch: s.Storage.Batch(), check: s.check}
}

func (s *sealGatedStorage) Snapshot() (storage.Snapshot, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	snap, err := s.Storage.Snapshot()
	if err != nil {
		return nil, err
	}
	return &sealGatedSnapshot{Snapshot: snap, check: s.check}, nil
}

// sealGatedBatch refuses the batch's reads and its commit once sealed.
type sealGatedBatch struct {
	storage.Batch
	check func() error
}

func (b *sealGatedBatch) Get(key []byte) ([]byte, error) {
	if err := b.check(); err != nil {
		return nil, err
	}
	return b.Batch.Get(key)
}

func (b *sealGatedBatch) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	return gatedIterator(b.Batch.NewIterator(opts), b.check)
}

func (b *sealGatedBatch) Commit() error {
	if err := b.check(); err != nil {
		return err
	}
	return b.Batch.Commit()
}

// sealGatedSnapshot refuses a snapshot's reads once sealed.
type sealGatedSnapshot struct {
	storage.Snapshot
	check func() error
}

func (s *sealGatedSnapshot) Get(key []byte) ([]byte, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	return s.Snapshot.Get(key)
}

func (s *sealGatedSnapshot) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	return gatedIterator(s.Snapshot.NewIterator(opts), s.check)
}

// sealGatedIterator positions nowhere and reports the gate's error once
// sealed, so a scan ends with the error instead of reading as empty.
type sealGatedIterator struct {
	storage.Iterator
	err error
}

func gatedIterator(it storage.Iterator, check func() error) storage.Iterator {
	if err := check(); err != nil {
		return &sealGatedIterator{Iterator: it, err: err}
	}
	return it
}

func (it *sealGatedIterator) SeekToFirst() bool  { return false }
func (it *sealGatedIterator) SeekToLast() bool   { return false }
func (it *sealGatedIterator) Seek(_ []byte) bool { return false }
func (it *sealGatedIterator) Next() bool         { return false }
func (it *sealGatedIterator) Prev() bool         { return false }
func (it *sealGatedIterator) Valid() bool        { return false }
func (it *sealGatedIterator) Error() error       { return it.err }

// bindingStorageConfig returns the storage configuration a binding's store
// opens with. An objstore database on the local provider lives under its
// binding root, so the copy drops the configured "path", which places the
// node's own store only, and resolveObjstoreSettings roots the store at
// <DataRoot>/objstore. Every other backend gets cfg unchanged; a remote
// provider never reaches here (Create refuses it with a
// *RemoteObjstoreBindingUnsupportedError). cfg's settings map is never
// mutated.
func bindingStorageConfig(cfg qrdbconfig.StorageConfig, b *database.Binding) qrdbconfig.StorageConfig {
	// A configured wal_dir is the node store's: every binding writes its
	// write-ahead log in a subdirectory of its own (BindingWALDir), so no
	// two Pebble databases replay or recycle one directory's logs.
	if b.Backend == database.BackendPebble && cfg.WALDir != "" {
		cfg.WALDir = BindingWALDir(cfg.WALDir, b.DataRoot)
	}
	if b.Backend != database.BackendObjstore {
		return cfg
	}
	settings := make(map[string]string, len(cfg.Objstore.Settings))
	for k, v := range cfg.Objstore.Settings {
		if k != "path" {
			settings[k] = v
		}
	}
	cfg.Objstore.Settings = settings
	return cfg
}

// BindingWALDir returns the write-ahead log directory of the Pebble binding
// store rooted at dataRoot when the node's storage.wal_dir is walDir: a
// subdirectory of walDir named for the binding root, which no two live
// bindings share (database.Manager refuses a root another record claims).
// An empty walDir keeps the log beside the data, under dataRoot, and returns
// "".
func BindingWALDir(walDir, dataRoot string) string {
	if walDir == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(filepath.Clean(dataRoot)))
	return filepath.Join(walDir, "bindings", hex.EncodeToString(sum[:16]))
}

// clearStaleBindingWAL removes a binding's WAL directory when its store has
// no Pebble data yet. A drop removes the WAL directory (ExternalDirs) after
// the binding root, so a crash between the two leaves a log with no store;
// a database opened again under the same root must not replay it.
func clearStaleBindingWAL(walDir, dataRoot string) error {
	if walDir == "" {
		return nil
	}
	_, err := os.Stat(filepath.Join(dataRoot, "pebble"))
	if err == nil {
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.RemoveAll(walDir)
}

// MaterializedBinding is a database's own store, engine and service host.
// It is a database.Backend (over the same store) for the Manager's own uses,
// and the registry serves the database's data plane from Host.
type MaterializedBinding struct {
	*database.StandaloneBackend

	local   *backend.LocalBackend
	stg     storage.Storage
	hub     *watch.Hub
	binding *database.Binding
	// walDir is the Pebble store's write-ahead log directory outside its
	// root (BindingWALDir), "" when the log lives under the root.
	walDir string
	// vector is the database's own vector service; closeVector stops it
	// and closes its storage.
	vector      service.VectorServicer
	closeVector func() error
	stop        func()
	closeOnce   sync.Once
	closeErr    error

	// barrier is the database barrier layered on stg, nil for a binding
	// without one. build opens the engine, host and vector service; Ready
	// runs it once, under mu, and ready records that it ran.
	barrier *databaseBarrierStorage
	build   func() error
	mu      sync.Mutex
	ready   atomic.Bool
	closed  bool
}

// Host returns the database's own service host. A barrier binding whose
// barrier is sealed returns a host that refuses every call as sealed.
func (m *MaterializedBinding) Host() service.Host {
	if err := m.Ready(); err != nil {
		return sealedHost{id: m.binding.Database, err: err}
	}
	return m.local
}

// ShardID returns the shard the database's data lives on in Host.
func (m *MaterializedBinding) ShardID() uint64 { return shard.ShardKV }

// WatchHub returns the hub the database's engine publishes to.
func (m *MaterializedBinding) WatchHub() *watch.Hub { return m.hub }

// Binding returns the resolved binding.
func (m *MaterializedBinding) Binding() *database.Binding { return m.binding }

// DataRoot returns the binding's store directory; it is empty for memory.
func (m *MaterializedBinding) DataRoot() string { return m.binding.DataRoot }

// ExternalDirs returns the directories the binding's store writes outside
// its DataRoot (database.ExternalDirOwner): a Pebble binding's write-ahead
// log when the node sets storage.wal_dir (BindingWALDir). A drop removes
// them with the root.
func (m *MaterializedBinding) ExternalDirs() []string {
	if m.walDir == "" {
		return nil
	}
	return []string{m.walDir}
}

// Close stops the lease sweeper, closes the host, then the store. It runs
// once; later calls return the first result.
func (m *MaterializedBinding) Close() error {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.closed = true
		// The barrier store's lease ends first: a seal no longer waits on
		// a store that is closing, and a write it admitted keeps the drain
		// waiting until it returns.
		if m.barrier != nil {
			m.barrier.detach()
		}
		if m.stop != nil {
			m.stop()
		}
		// The hub is this database's own: closing it cancels its watchers
		// and frees their node-wide WatcherLimit slots, and refuses new
		// watches against the dropped database.
		if m.hub != nil {
			m.hub.Close()
		}
		var vecErr error
		if m.closeVector != nil {
			vecErr = m.closeVector()
		}
		var hostErr error
		if m.local != nil {
			hostErr = m.local.Close()
		}
		m.closeErr = errors.Join(vecErr, hostErr, m.StandaloneBackend.Close())
	})
	return m.closeErr
}

// Vector returns the database's own vector service, built over storage
// under its binding root; nil while a barrier binding is sealed.
func (m *MaterializedBinding) Vector() service.VectorServicer {
	if m.Ready() != nil {
		return nil
	}
	return m.vector
}

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
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

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
	vectorstorage "github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
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
// tenant's barrier for a barrier tenant.
type LocalBindingFactory struct {
	node       database.BindingNode
	enc        *atrest.Encryptor
	tenantEnc  func(tenantID string) (*atrest.Encryptor, error)
	storageCfg qrdbconfig.StorageConfig
	tenants    func(tenantID string) (database.BindingTenant, error)
	fallback   database.BackendFactory
	hubOpts    []watch.HubOption
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
// per-database barrier, raft mode, and volume and raft tenants stay
// delegated to the embedded factory. hubOpts configure the watch
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

// materializes reports whether a tenant of isolation gets its databases
// materialized: a prefix tenant (or an unset isolation) always, a barrier
// tenant only with a tenant encryptor lookup.
func (f *LocalBindingFactory) materializes(isolation tenant.IsolationType) bool {
	switch isolation {
	case "", tenant.IsolationPrefix:
		return true
	case tenant.IsolationBarrier:
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
	// A database recorded before the binding change keeps its data in the
	// store it was created with, at dataDir: while its binding root does
	// not exist and that store holds data, it opens there as before, not
	// as a new, empty materialized store. Moving it to the binding root is
	// a relocation (slice 3b.8), not done here. A fresh create has no data
	// at dataDir and gets its materialized binding.
	if preBindingData(b.DataRoot, dataDir) {
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
	if !f.node.RaftMode && cfg.Backend == database.BackendRaft {
		return "", nil
	}
	b, err := f.resolveLocal(&cfg)
	if err != nil || b == nil {
		return "", err
	}
	return b.DataRoot, nil
}

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
	if b.Kind != database.BindingLocal || b.DatabaseBarrier {
		return nil, nil
	}
	if b.Backend == database.BackendObjstore {
		if p := f.storageCfg.Objstore.Provider; p != "" && p != "local" {
			return nil, &RemoteObjstoreBindingUnsupportedError{TenantID: b.TenantID, Database: b.Database, Provider: p}
		}
	}
	return b, nil
}

// preBindingData reports a database whose data predates its binding: the
// binding root does not exist, and the store at dataDir, where the database
// was opened before, holds data. A memory store (no root) has nothing at
// rest to keep.
func preBindingData(root, dataDir string) bool {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(dataDir) == "" {
		return false
	}
	if filepath.Clean(root) == filepath.Clean(dataDir) {
		return false
	}
	if _, err := os.Stat(root); !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	entries, err := os.ReadDir(dataDir)
	return err == nil && len(entries) > 0
}

// Close releases the embedded factory.
func (f *LocalBindingFactory) Close() error {
	return f.fallback.Close()
}

// mayQualify reports the checks that need no tenant lookup.
func (f *LocalBindingFactory) mayQualify(cfg *database.DatabaseConfig) bool {
	if f.node.RaftMode {
		return false
	}
	// The system database lives on the control-plane store.
	if tenantid.IsSystem(cfg.TenantID) && cfg.Name == SystemDatabase {
		return false
	}
	if database.IsTenantHome(cfg.Name) {
		return false
	}
	return cfg.Backend != database.BackendRaft
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
// BindingOpenError). DataRoot stays empty: a volume or raft tenant is
// delegated to the fallback factory before it would be read.
func TenantServiceBindingLookup(get func() tenant.TenantServicer) func(string) (database.BindingTenant, error) {
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
		return database.BindingTenant{ID: tenantID, Isolation: info.EffectiveIsolationType()}, nil
	}
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
		TenantServiceBindingLookup(tenants), hubOpts...).
		WithTenantEncryptors(TenantBarrierEncryptorLookup(barriers))
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
	if b.Backend == database.BackendObjstore && enc.Enabled() {
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
	hub := watch.NewHub(f.hubOpts...)
	lr := lease.NewLessor()
	eng, err := NewLocalEngine(b.Backend, stg, pdb, hub, lr)
	if err != nil {
		return nil, openErr(errors.Join(err, stg.Close()))
	}
	local := backend.NewLocalBackend(eng)
	vec, closeVec, err := newBindingVector(b, enc, local)
	if err != nil {
		return nil, openErr(errors.Join(err, local.Close(), stg.Close()))
	}
	stop := StartLocalLeaseExpiry(context.Background(), lr, service.NewLeaseService(local, shard.ShardKV), slog.Default())
	return &MaterializedBinding{
		StandaloneBackend: database.NewStandaloneBackend(stg),
		local:             local,
		stg:               stg,
		hub:               hub,
		binding:           b,
		vector:            vec,
		closeVector:       closeVec,
		stop:              stop,
	}, nil
}

// newBindingVector builds the binding's own vector service, over storage
// under the binding root, with the constructor the node's vector service
// uses (service.NewVectorService, the qrdb_vector build-tag seam). A
// memory binding keeps its vectors in memory, as its KV; every other
// backend keeps them in a Pebble store under <root>/vector, encrypted with
// the binding's encryptor. The returned close stops the service before the
// storage beneath it.
func newBindingVector(b *database.Binding, enc *atrest.Encryptor, host any) (service.VectorServicer, func() error, error) {
	log := logger.NewAdapter(newMinimalLogger(slog.Default()))
	var vs vectorstorage.Storage
	if b.Backend == database.BackendMemory || b.DataRoot == "" {
		vs = vectorstorage.NewMemoryVectorStorage()
	} else {
		pvs, err := vectorstorage.NewPebbleVectorStorage(vectorstorage.DefaultConfig(filepath.Join(b.DataRoot, "vector")), log, vectorstorage.WithEncryptor(enc))
		if err != nil {
			return nil, nil, &VectorStorageError{Err: err}
		}
		vs = pvs
	}
	var opts []service.VectorServiceOption
	if gates, ok := host.(service.FeatureGatedHost); ok {
		opts = append(opts, service.WithFeatureGates(gates))
	}
	svc, err := service.NewVectorService(vs, log, opts...)
	if err != nil {
		return nil, nil, errors.Join(&VectorServiceError{Err: err}, vs.Close())
	}
	closeFn := func() error {
		var svcErr error
		if c, ok := svc.(io.Closer); ok {
			svcErr = c.Close()
		}
		return errors.Join(svcErr, vs.Close())
	}
	return svc, closeFn, nil
}

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
// no Pebble data yet: the binding root of a dropped database is removed
// while its WAL directory, outside that root, is not, so a database created
// again under the same root must not replay the dropped one's logs.
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
	// vector is the database's own vector service; closeVector stops it
	// and closes its storage.
	vector      service.VectorServicer
	closeVector func() error
	stop        func()
	closeOnce   sync.Once
	closeErr    error
}

// Host returns the database's own service host.
func (m *MaterializedBinding) Host() service.Host { return m.local }

// ShardID returns the shard the database's data lives on in Host.
func (m *MaterializedBinding) ShardID() uint64 { return shard.ShardKV }

// WatchHub returns the hub the database's engine publishes to.
func (m *MaterializedBinding) WatchHub() *watch.Hub { return m.hub }

// Binding returns the resolved binding.
func (m *MaterializedBinding) Binding() *database.Binding { return m.binding }

// DataRoot returns the binding's store directory; it is empty for memory.
func (m *MaterializedBinding) DataRoot() string { return m.binding.DataRoot }

// Close stops the lease sweeper, closes the host, then the store. It runs
// once; later calls return the first result.
func (m *MaterializedBinding) Close() error {
	m.closeOnce.Do(func() {
		m.stop()
		var vecErr error
		if m.closeVector != nil {
			vecErr = m.closeVector()
		}
		m.closeErr = errors.Join(vecErr, m.local.Close(), m.StandaloneBackend.Close())
	})
	return m.closeErr
}

// Vector returns the database's own vector service, built over storage
// under its binding root.
func (m *MaterializedBinding) Vector() service.VectorServicer { return m.vector }

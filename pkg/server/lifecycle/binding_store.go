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
	"errors"
	"log/slog"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
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
	// A standalone node has no Raft host: a backend: raft record is
	// refused, on create and when a recorded database is provisioned, the
	// way ResolveBinding refuses it, never served from an unreplicated
	// local store.
	if !f.node.RaftMode && cfg.Backend == database.BackendRaft {
		return nil, &database.RaftBackendUnavailableError{Name: cfg.Name}
	}
	if !f.mayQualify(&cfg) {
		return f.fallback.Create(cfg, dataDir)
	}
	t, err := f.tenant(cfg.TenantID)
	if err != nil {
		return nil, &BindingOpenError{TenantID: cfg.TenantID, Database: cfg.Name, Backend: cfg.Backend, Err: err}
	}
	if !f.materializes(t.Isolation) {
		return f.fallback.Create(cfg, dataDir)
	}
	b, err := database.ResolveBinding(&cfg, t, f.node)
	if err != nil {
		return nil, err
	}
	if b.Kind != database.BindingLocal || b.DatabaseBarrier {
		return f.fallback.Create(cfg, dataDir)
	}
	if b.Backend == database.BackendObjstore {
		if p := f.storageCfg.Objstore.Provider; p != "" && p != "local" {
			return nil, &RemoteObjstoreBindingUnsupportedError{TenantID: b.TenantID, Database: b.Database, Provider: p}
		}
	}
	return f.open(b)
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
	stg, pdb, err := NewLocalStorage(b.Backend, b.DataRoot, enc, bindingStorageConfig(f.storageCfg, b))
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
	stop := StartLocalLeaseExpiry(context.Background(), lr, service.NewLeaseService(local, shard.ShardKV), slog.Default())
	return &MaterializedBinding{
		StandaloneBackend: database.NewStandaloneBackend(stg),
		local:             local,
		stg:               stg,
		hub:               hub,
		binding:           b,
		stop:              stop,
	}, nil
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

// MaterializedBinding is a database's own store, engine and service host.
// It is a database.Backend (over the same store) for the Manager's own uses,
// and the registry serves the database's data plane from Host.
type MaterializedBinding struct {
	*database.StandaloneBackend

	local     *backend.LocalBackend
	stg       storage.Storage
	hub       *watch.Hub
	binding   *database.Binding
	stop      func()
	closeOnce sync.Once
	closeErr  error
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
		m.closeErr = errors.Join(m.local.Close(), m.StandaloneBackend.Close())
	})
	return m.closeErr
}

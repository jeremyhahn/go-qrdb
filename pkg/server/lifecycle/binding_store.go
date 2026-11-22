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
// This part materializes only the plainest binding: a prefix-isolated
// tenant's database on a standalone node, with no database barrier, no
// explicit data_dir and not in the system tenant. Every other database is
// delegated unchanged to database.NewEmbeddedBackendFactory (docs/plans/923.md
// slice 3b widens the set).

// LocalBindingFactory is the database.BackendFactory of a standalone node
// that opens a qualifying database's own store under its binding root.
type LocalBindingFactory struct {
	node       database.BindingNode
	enc        *atrest.Encryptor
	storageCfg qrdbconfig.StorageConfig
	tenants    func(tenantID string) (database.BindingTenant, error)
	fallback   database.BackendFactory
	hubOpts    []watch.HubOption
}

// NewLocalBindingFactory returns a factory for node. enc is the node
// encryptor every on-disk binding store writes through (nil or disabled
// leaves them plaintext); storageCfg is the node's storage configuration
// (NewLocalStorage). tenants looks up the owning tenant of a database; a nil
// tenants treats every tenant as prefix-isolated. hubOpts configure the watch
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

// Create opens cfg's backend. A qualifying database gets a
// *MaterializedBinding over its own store; any other is opened by the
// embedded factory at dataDir, unchanged.
func (f *LocalBindingFactory) Create(cfg database.DatabaseConfig, dataDir string) (database.Backend, error) {
	if !f.mayQualify(&cfg) {
		return f.fallback.Create(cfg, dataDir)
	}
	t, err := f.tenant(cfg.TenantID)
	if err != nil {
		return nil, &BindingOpenError{TenantID: cfg.TenantID, Database: cfg.Name, Backend: cfg.Backend, Err: err}
	}
	if t.Isolation != "" && t.Isolation != tenant.IsolationPrefix {
		return f.fallback.Create(cfg, dataDir)
	}
	b, err := database.ResolveBinding(&cfg, t, f.node)
	if err != nil {
		return nil, err
	}
	if b.Kind != database.BindingLocal || b.DatabaseBarrier {
		return f.fallback.Create(cfg, dataDir)
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
	if cfg.TenantID == "" || tenantid.IsSystem(cfg.TenantID) {
		return false
	}
	if database.IsTenantHome(cfg.Name) {
		return false
	}
	return !cfg.HasDedicatedStore()
}

func (f *LocalBindingFactory) tenant(tenantID string) (database.BindingTenant, error) {
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

func (f *LocalBindingFactory) open(b *database.Binding) (*MaterializedBinding, error) {
	openErr := func(err error) error {
		return &BindingOpenError{TenantID: b.TenantID, Database: b.Database, Backend: b.Backend, Err: err}
	}
	stg, pdb, err := NewLocalStorage(b.Backend, b.DataRoot, f.enc, f.storageCfg)
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

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

package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	qrdbbackend "github.com/jeremyhahn/go-qrdb/pkg/backend"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// createDatabase creates a kv database on the node through its client.
func createDatabase(ctx context.Context, t *testing.T, n *Node, name string) {
	t.Helper()
	createDatabaseOn(ctx, t, n, name, database.BackendMemory)
}

// createDatabaseOn creates the database name on backend. A database is
// materialized in its own store, so only a persistent backend keeps its data
// across a restart: a memory database is, by its contract, non-persistent.
func createDatabaseOn(ctx context.Context, t *testing.T, n *Node, name string, backend database.BackendType) {
	t.Helper()
	_, err := n.Client().Database().CreateDatabase(ctx, transport.CreateDatabaseRequest{
		Name: name, Backend: string(backend),
	})
	require.NoError(t, err)
}

// A standalone node serves its named databases: each resolves for a scoped
// client and their keyspaces are disjoint. The node used to compose its
// server with neither the database catalog nor a database service registry,
// so no named database resolved at all and every scoped call was refused.
func TestStandaloneNode_NamedDatabasesResolve(t *testing.T) {
	ctx := context.Background()
	n := newSharedTenantNode(ctx, t)
	createDatabase(ctx, t, n, "ledger")
	createDatabase(ctx, t, n, "archive")

	ledger, err := n.Client().DatabaseScoped("ledger")
	require.NoError(t, err)
	archive, err := n.Client().DatabaseScoped("archive")
	require.NoError(t, err)

	require.NoError(t, ledger.KV().Put(ctx, "k", []byte("ledger")))
	require.NoError(t, archive.KV().Put(ctx, "k", []byte("archive")))

	got, err := ledger.KV().Get(ctx, "k")
	require.NoError(t, err)
	require.Equal(t, "ledger", string(got))
	got, err = archive.KV().Get(ctx, "k")
	require.NoError(t, err)
	require.Equal(t, "archive", string(got))
	_, err = n.Client().KV().Get(ctx, "k")
	require.Error(t, err, "a scoped write landed in the node's default database")

	unknown, err := n.Client().DatabaseScoped("nosuchdb")
	require.NoError(t, err, "scoping resolves per call; the refusal comes at the call")
	require.Error(t, unknown.KV().Put(ctx, "k", []byte("v")), "an unknown database accepted a write")
}

// A named database lives in its own store, and its services are built with
// the node's configured limits: index.max_indexes holds there as it does on
// the node's default database, rather than the package default (unlimited).
func TestStandaloneNode_MaterializedDatabaseKeepsTheIndexLimit(t *testing.T) {
	ctx := context.Background()
	cfg := newTenantIsolationConfig(t)
	cfg.Index.MaxIndexes = 1
	n, err := NewStandaloneNode(ctx, cfg, nil, WithTenantIsolation())
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	createDatabase(ctx, t, n, "ledger")

	ledger, err := n.Client().DatabaseScoped("ledger")
	require.NoError(t, err)
	require.NoError(t, ledger.Index().IndexCreate(ctx, "by_a", "a", transport.IndexConfig{}))
	require.Error(t, ledger.Index().IndexCreate(ctx, "by_b", "b", transport.IndexConfig{}),
		"a materialized database accepted an index past index.max_indexes")
}

// The embedded leg of the sealed-database refusal the network transports are
// held to (pkg/server TestSealedDatabase_RefusedOnEveryTransport): a client
// scoped to a database through the node's embedded transport is refused
// once that database is sealed, on the next call, by an already-scoped
// client -- the scoped transport resolves the database through the running
// server's registry on every call.
func TestEmbedded_SealedDatabaseRefused(t *testing.T) {
	ctx := context.Background()
	n := newSharedTenantNode(ctx, t)
	createDatabase(ctx, t, n, "ledger")
	scoped, err := n.Client().DatabaseScoped("ledger")
	require.NoError(t, err)
	require.NoError(t, scoped.KV().Put(ctx, "k", []byte("v")), "an open database accepts a write")

	db, err := n.dbManager.Get("ledger")
	require.NoError(t, err)
	db.SetStatus(database.StatusSealed)

	var sealed *service.BarrierSealedError
	err = scoped.KV().Put(ctx, "k2", []byte("v"))
	require.Truef(t, errors.As(err, &sealed), "Put on a sealed database = %v, want *service.BarrierSealedError", err)
	_, err = scoped.KV().Get(ctx, "k")
	require.Truef(t, errors.As(err, &sealed), "Get on a sealed database = %v, want *service.BarrierSealedError", err)

	db.SetStatus(database.StatusOpen)
	require.NoError(t, scoped.KV().Put(ctx, "k3", []byte("v")), "unsealing restores access")
}

// A database created on a standalone node outlives the node: the catalog is
// reopened at the next boot, so the database still resolves and keeps its
// data. The catalog used to be built fresh inside the service graph and never
// reopened a previous run's databases.
func TestStandaloneNode_DatabasesSurviveRestart(t *testing.T) {
	ctx := context.Background()
	cfg := newTenantIsolationConfig(t)
	n, err := NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	createDatabaseOn(ctx, t, n, "ledger", database.BackendPebble)
	scoped, err := n.Client().DatabaseScoped("ledger")
	require.NoError(t, err)
	require.NoError(t, scoped.KV().Put(ctx, "k", []byte("v")))
	require.NoError(t, n.Close())

	n, err = NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	scoped, err = n.Client().DatabaseScoped("ledger")
	require.NoError(t, err)
	got, err := scoped.KV().Get(ctx, "k")
	require.NoError(t, err, "the database did not survive the restart")
	require.Equal(t, "v", string(got))
}

// TestStandaloneNode_DatabaseLivesUnderItsBindingRoot pins that the standalone
// node opens a database through the shared binding factory: a pebble database
// of a prefix tenant is materialized in its own store under its binding root
// (<data_dir>/tenants/<tenant>/databases/<name>), and the catalog reopens that
// store on the next boot, so what was written is read back.
func TestStandaloneNode_DatabaseLivesUnderItsBindingRoot(t *testing.T) {
	ctx := context.Background()
	cfg := newTenantIsolationConfig(t)
	n, err := NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	createDatabaseOn(ctx, t, n, "orders", database.BackendPebble)
	scoped, err := n.Client().DatabaseScoped("orders")
	require.NoError(t, err)
	require.NoError(t, scoped.KV().Put(ctx, "order/1", []byte("open")))
	require.NoError(t, n.Close())

	root := filepath.Join(cfg.Raft.DataDir, "tenants", tenantid.System, "databases", "orders")
	info, err := os.Stat(root)
	require.NoError(t, err, "the database has no store under its binding root")
	require.True(t, info.IsDir())

	n, err = NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	scoped, err = n.Client().DatabaseScoped("orders")
	require.NoError(t, err)
	got, err := scoped.KV().Get(ctx, "order/1")
	require.NoError(t, err, "the materialized database was not reopened from its binding root")
	require.Equal(t, "open", string(got))
}

// seedSharedStore writes key under the system tenant's legacy database
// namespace straight into a stopped standalone node's shared store, opened
// with the constructors NewStandaloneNode uses for it (createNodeStorage
// and createNodeEngine with the pebble backend at raft.data_dir and no
// encryptor, as a node with seal disabled opens it). It stands
// in for data written before the database had a store of its own.
func seedSharedStore(ctx context.Context, t *testing.T, cfg *qrdbconfig.Config, db, key, value string) {
	t.Helper()
	stg, pdb, err := lifecycle.NewLocalStorage(database.BackendPebble, cfg.Raft.DataDir, nil, cfg.Storage)
	require.NoError(t, err)
	eng, err := lifecycle.NewLocalEngine(database.BackendPebble, stg, pdb, watch.NewHub(), lease.NewLessor())
	if err != nil {
		_ = stg.Close()
		require.NoError(t, err)
	}
	host := qrdbbackend.NewLocalBackend(eng)
	full := tenant.ComposeNamespace(string(tenantid.System), db) + key
	putErr := service.NewKVService(host, shard.ShardKV).Put(ctx, full, []byte(value))
	_ = host.Close()
	require.NoError(t, stg.Close())
	require.NoError(t, putErr)
}

// TestStandaloneNode_LegacyMemoryDatabaseKeepsSharedRoute pins the
// standalone node's shared-keyspace probe (newStandaloneDatabaseManager's
// lifecycle.WithSharedKeyspaceProbe): a backend: memory database whose
// data predates its own store keeps reading the shared store, a fresh
// memory database is materialized and sees none of it, and the legacy data
// survives a restart, which a materialized memory store would not.
func TestStandaloneNode_LegacyMemoryDatabaseKeepsSharedRoute(t *testing.T) {
	ctx := context.Background()
	cfg := newTenantIsolationConfig(t)
	// Seal off: the shared store is written without an at-rest encryptor
	// (the strategy-none barrier still frames its files), so the test can
	// seed it offline. The sealed boot path builds its database manager
	// through the same newStandaloneDatabaseManager and probe.
	cfg.Seal.Enabled = false
	n, err := NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	require.NoError(t, n.Close())

	seedSharedStore(ctx, t, cfg, "legacy", "k", "shared-v")

	n, err = NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	createDatabaseOn(ctx, t, n, "legacy", database.BackendMemory)
	createDatabaseOn(ctx, t, n, "fresh", database.BackendMemory)
	legacy, err := n.Client().DatabaseScoped("legacy")
	require.NoError(t, err)
	got, err := legacy.KV().Get(ctx, "k")
	require.NoError(t, err, "the legacy memory database lost its shared route")
	require.Equal(t, "shared-v", string(got))
	fresh, err := n.Client().DatabaseScoped("fresh")
	require.NoError(t, err)
	_, err = fresh.KV().Get(ctx, "k")
	require.Error(t, err, "a fresh memory database read another database's shared data")
	require.NoError(t, n.Close())

	n, err = NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	legacy, err = n.Client().DatabaseScoped("legacy")
	require.NoError(t, err)
	got, err = legacy.KV().Get(ctx, "k")
	require.NoError(t, err, "the legacy key did not survive the restart")
	require.Equal(t, "shared-v", string(got))
}

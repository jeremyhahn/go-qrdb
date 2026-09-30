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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// createDatabase creates a kv database on the node through its client.
func createDatabase(ctx context.Context, t *testing.T, n *Node, name string) {
	t.Helper()
	_, err := n.Client().Database().CreateDatabase(ctx, transport.CreateDatabaseRequest{
		Name: name, Engine: string(database.EngineKV), Backend: string(database.BackendMemory),
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
	createDatabase(ctx, t, n, "ledger")
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

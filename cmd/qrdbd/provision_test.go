// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// provisionReclaimer is a keyspace reclaimer whose fence marker behaves the
// way the store's does: a drop leaves the tombstone, and only an unfence
// clears it.
type provisionReclaimer struct {
	marker map[string]string
}

const provisionTombstone = "\x00dropped"

func (r *provisionReclaimer) FenceDatabaseKeyspace(_ context.Context, tenantID, db string) error {
	r.marker[tenantID+"/"+db] = provisionTombstone
	return nil
}

func (r *provisionReclaimer) UnfenceDatabaseKeyspace(_ context.Context, tenantID, db, incarnation string) error {
	key := tenantID + "/" + db
	if current := r.marker[key]; current != "" && current != provisionTombstone && current != incarnation {
		return &database.AlreadyExistsError{Name: db, Elsewhere: true}
	}
	r.marker[key] = incarnation
	return nil
}

func (r *provisionReclaimer) ReclaimDatabaseKeyspace(context.Context, string, string) error {
	return nil
}

var _ database.KeyspaceReclaimer = (*provisionReclaimer)(nil)

func provisionFlags(sqlDatabase string) *startFlags {
	return &startFlags{
		databases:   []qrdbconfig.DatabaseConfig{{Name: "orders", Engine: "kv", Backend: "memory"}},
		sqlDatabase: sqlDatabase,
		enableMySQL: sqlDatabase != "",
		smType:      "memory",
	}
}

// A configured database dropped on an earlier boot is re-provisioned once
// the reclaimer is attached -- which is why start runs provisioning after
// BuildServices -- and refused with a typed error before it, rather than
// created with its tombstone refusing every write.
func TestProvisionDatabases_DroppedConfiguredNameNeedsTheReclaimer(t *testing.T) {
	dir := t.TempDir()
	reclaimer := &provisionReclaimer{marker: map[string]string{}}
	mgr := database.NewManager(nil, dir)
	mgr.SetKeyspaceReclaimer(reclaimer)
	require.NoError(t, provisionDatabases(context.Background(), provisionFlags(""), mgr, electedLeader{}))
	require.NoError(t, mgr.Drop(context.Background(), "orders"))
	require.NoError(t, mgr.Close())

	restarted := database.NewManager(nil, dir)
	t.Cleanup(func() { _ = restarted.Close() })
	require.NoError(t, restarted.OpenAll())
	err := provisionDatabases(context.Background(), provisionFlags(""), restarted, electedLeader{})
	var required *database.ReclaimerRequiredError
	require.ErrorAs(t, err, &required)

	restarted.SetKeyspaceReclaimer(reclaimer)
	require.NoError(t, provisionDatabases(context.Background(), provisionFlags(""), restarted, electedLeader{}))
	_, err = restarted.Get("orders")
	require.NoError(t, err)
	assert.Empty(t, reclaimer.marker["/orders"], "provisioning must clear the tombstone")
}

// --sql-database is created with the enabled SQL protocols, and re-running
// provisioning (the in-process recycle) keeps it and its protocols.
func TestProvisionDatabases_CreatesAndKeepsTheSQLDatabase(t *testing.T) {
	mgr := database.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	f := provisionFlags("shop")
	require.NoError(t, provisionDatabases(context.Background(), f, mgr, electedLeader{}))
	db, err := mgr.Get("shop")
	require.NoError(t, err)
	assert.True(t, db.ProtocolConfig().MySQL)
	assert.Equal(t, database.BackendMemory, db.Config().Backend)

	f.enablePgSQL = true
	require.NoError(t, provisionDatabases(context.Background(), f, mgr, electedLeader{}))
	again, err := mgr.Get("shop")
	require.NoError(t, err)
	assert.Same(t, db, again)
	assert.True(t, again.ProtocolConfig().PostgreSQL, "an existing SQL database gains the enabled protocols")
}

// No SQL protocol enabled means --sql-database is not provisioned.
func TestProvisionDatabases_SkipsTheSQLDatabaseWithoutASQLProtocol(t *testing.T) {
	mgr := database.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	f := provisionFlags("shop")
	f.enableMySQL = false
	require.NoError(t, provisionDatabases(context.Background(), f, mgr, electedLeader{}))
	var notFound *database.NotFoundError
	_, err := mgr.Get("shop")
	assert.ErrorAs(t, err, &notFound)
}

// A databases[] entry's own mysql/postgresql flags carry through to the
// provisioned database -- previously configuredDatabases built Name/
// Engine/Backend/SQL only, silently dropping them regardless of build
// tags, every boot.
func TestProvisionDatabases_ConfiguredEntryCarriesItsOwnProtocol(t *testing.T) {
	mgr := database.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	f := &startFlags{
		databases: []qrdbconfig.DatabaseConfig{
			{Name: "orders", Engine: "kv", Backend: "memory", MySQL: true, PostgreSQL: true},
		},
		smType: "memory",
	}
	require.NoError(t, provisionDatabases(context.Background(), f, mgr, electedLeader{}))
	db, err := mgr.Get("orders")
	require.NoError(t, err)
	assert.True(t, db.ProtocolConfig().MySQL, "configured mysql: true was dropped")
	assert.True(t, db.ProtocolConfig().PostgreSQL, "configured postgresql: true was dropped")
}

// A databases[] entry enabling etcd on a binary without qrdb_etcd is
// refused at boot, before anything is provisioned -- the one caller
// service.RefuseEtcdNotCompiled's placement in pkg/service cannot reach on
// its own, because this path creates databases through *database.Manager
// directly.
func TestProvisionDatabases_ConfiguredEtcdCapabilityGate(t *testing.T) {
	mgr := database.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	f := &startFlags{
		databases: []qrdbconfig.DatabaseConfig{
			{Name: "locks", Engine: "kv", Backend: "memory", Etcd: &qrdbconfig.DatabaseEtcdConfig{
				Enabled: true, ListenAddr: "127.0.0.1:2379",
			}},
		},
		smType: "memory",
	}
	err := provisionDatabases(context.Background(), f, mgr, electedLeader{})

	if !capabilities.Has("etcd") {
		var capErr *service.CapabilityUnsupportedError
		require.ErrorAs(t, err, &capErr, "want *service.CapabilityUnsupportedError, got %T: %v", err, err)
		var notFound *database.NotFoundError
		_, getErr := mgr.Get("locks")
		assert.ErrorAs(t, getErr, &notFound, "the refused database must not have been provisioned")
		return
	}
	require.NoError(t, err)
	db, getErr := mgr.Get("locks")
	require.NoError(t, getErr)
	require.NotNil(t, db.ProtocolConfig().Etcd)
	assert.True(t, db.ProtocolConfig().Etcd.Enabled)
}

// A --sql-database a drop is fencing does not fail boot; it stays fenced.
func TestProvisionDatabases_LeavesADroppingSQLDatabaseFenced(t *testing.T) {
	mgr := database.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	f := provisionFlags("shop")
	require.NoError(t, provisionDatabases(context.Background(), f, mgr, electedLeader{}))
	db, err := mgr.Get("shop")
	require.NoError(t, err)
	db.SetDropping(true)

	require.NoError(t, provisionDatabases(context.Background(), f, mgr, electedLeader{}))
	var dropping *database.DroppingError
	_, err = mgr.Get("shop")
	assert.ErrorAs(t, err, &dropping)
}

// A lookup failure other than not-found or dropping is returned.
func TestProvisionDatabases_ReturnsAnUnexpectedLookupFailure(t *testing.T) {
	boom := errors.New("catalog unavailable")
	err := provisionDatabases(context.Background(), &startFlags{sqlDatabase: "shop", enablePgSQL: true}, failingProvisioner{err: boom}, electedLeader{})
	assert.ErrorIs(t, err, boom)
}

// failingProvisioner fails every lookup, standing in for a catalog read
// error no real manager produces on demand.
type failingProvisioner struct{ err error }

func (p failingProvisioner) Get(string) (*database.Database, error) { return nil, p.err }

func (p failingProvisioner) Provision(context.Context, database.DatabaseConfig) (*database.Database, error) {
	return nil, p.err
}

func (p failingProvisioner) UpdateProtocol(string, database.ProtocolConfig) error { return p.err }

func (p failingProvisioner) UpdateSQL(string, *database.SQLConfig) error { return p.err }

// electedLeader reports the system shard's leader as known, as every node
// does once the shard has elected one.
type electedLeader struct{}

func (electedLeader) GetLeaderID(uint64) (uint64, bool, error) { return 1, true, nil }

// noLeader reports no known leader, as a node does before its shard's first
// election.
type noLeader struct{}

func (noLeader) GetLeaderID(uint64) (uint64, bool, error) { return 0, false, nil }

// Provisioning writes the drop-fence marker through Raft, so it waits for a
// leader first -- a proposal before any election is refused, not queued --
// and a wait cut short is reported before anything is created.
func TestProvisionDatabases_WaitsForALeaderBeforeWriting(t *testing.T) {
	mgr := database.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := provisionDatabases(ctx, provisionFlags(""), mgr, noLeader{})
	var provErr *DatabaseProvisionError
	require.ErrorAs(t, err, &provErr)
	assert.Equal(t, "wait for leader", provErr.Step)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, provErr.Error(), "wait for leader")
	_, getErr := mgr.Get("orders")
	assert.Error(t, getErr, "nothing may be provisioned before a leader is known")
}

// awaitLeader returns as soon as the shard reports a leader.
func TestAwaitLeader_ReturnsOnceALeaderIsKnown(t *testing.T) {
	require.NoError(t, awaitLeader(context.Background(), electedLeader{}, 1))
}

// An invalid configured database is rejected up front, with the typed
// error its validation reports, and a valid configuration passes.
func TestValidateConfiguredDatabases(t *testing.T) {
	require.NoError(t, validateConfiguredDatabases(provisionFlags("")))
	var invalid *database.InvalidEngineError
	err := validateConfiguredDatabases(&startFlags{databases: []qrdbconfig.DatabaseConfig{{Name: "x", Engine: "bogus", Backend: "memory"}}})
	assert.ErrorAs(t, err, &invalid)
}

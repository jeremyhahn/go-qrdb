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

func (r *provisionReclaimer) ReclaimDatabaseKeyspace(context.Context, string, string, string) error {
	return nil
}

var _ database.KeyspaceReclaimer = (*provisionReclaimer)(nil)

func provisionFlags(sqlDatabase string) *startFlags {
	return &startFlags{
		databases:   []qrdbconfig.DatabaseConfig{{Name: "orders", Backend: "memory"}},
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
			{Name: "orders", Backend: "memory", MySQL: true, PostgreSQL: true},
		},
		smType: "memory",
	}
	require.NoError(t, provisionDatabases(context.Background(), f, mgr, electedLeader{}))
	db, err := mgr.Get("orders")
	require.NoError(t, err)
	assert.True(t, db.ProtocolConfig().MySQL, "configured mysql: true was dropped")
	assert.True(t, db.ProtocolConfig().PostgreSQL, "configured postgresql: true was dropped")
}

// sqlUpdateFailingManager is a real manager whose SQL-settings update fails.
type sqlUpdateFailingManager struct {
	*database.Manager
	err error
}

func (m sqlUpdateFailingManager) UpdateSQL(string, *database.SQLConfig) error { return m.err }

var _ databaseProvisioner = sqlUpdateFailingManager{}

// A databases[] entry's sql settings are applied on every boot, an existing
// database included, and a failure to apply them fails provisioning.
func TestProvisionDatabases_ConfiguredEntryAppliesItsSQLSettings(t *testing.T) {
	mgr := database.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	f := &startFlags{
		databases: []qrdbconfig.DatabaseConfig{{
			Name: "orders", Backend: "memory",
			SQL: &qrdbconfig.DatabaseSQLConfig{Concurrency: string(database.ConcurrencyPessimistic)},
		}},
		smType: "memory",
	}
	require.NoError(t, provisionDatabases(context.Background(), f, mgr, electedLeader{}))
	db, err := mgr.Get("orders")
	require.NoError(t, err)
	require.NotNil(t, db.SQLConfig())
	assert.Equal(t, database.ConcurrencyPessimistic, db.SQLConfig().Concurrency)

	boom := errors.New("sql settings refused")
	err = provisionDatabases(context.Background(), f, sqlUpdateFailingManager{Manager: mgr, err: boom}, electedLeader{})
	assert.ErrorIs(t, err, boom)
}

// A databases[] entry enabling etcd on a binary without qrdb_etcd is
// refused by validateConfiguredDatabases -- which start.go always calls
// before provisionDatabases, so this is where "every entry refuses before
// anything is provisioned" actually lives, not inside provisionDatabases'
// own per-entry loop. The valid entry is listed FIRST and the etcd one
// SECOND, so a check that only looked at the first entry would pass this
// test by accident.
func TestValidateConfiguredDatabases_RefusesEtcdNotCompiled(t *testing.T) {
	f := &startFlags{
		databases: []qrdbconfig.DatabaseConfig{
			{Name: "orders", Backend: "memory"},
			{Name: "locks", Backend: "memory", Etcd: &qrdbconfig.DatabaseEtcdConfig{
				Enabled: true, ListenAddr: "127.0.0.1:2379",
			}},
		},
	}
	err := validateConfiguredDatabases(f)

	if !capabilities.Has("etcd") {
		var capErr *service.CapabilityUnsupportedError
		require.ErrorAs(t, err, &capErr, "want *service.CapabilityUnsupportedError, got %T: %v", err, err)
		return
	}
	require.NoError(t, err)
}

// A configured database's etcd block carries through to provisioning.
// provisionDatabases itself does not re-check the capability -- that is
// validateConfiguredDatabases's job (see above), and start.go always runs
// it first -- so this is exercised the same way on every build.
func TestProvisionDatabases_ConfiguredEntryCarriesEtcd(t *testing.T) {
	mgr := database.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	f := &startFlags{
		databases: []qrdbconfig.DatabaseConfig{
			{Name: "locks", Backend: "memory", Etcd: &qrdbconfig.DatabaseEtcdConfig{
				Enabled: true, ListenAddr: "127.0.0.1:2379",
			}},
		},
		smType: "memory",
	}
	require.NoError(t, provisionDatabases(context.Background(), f, mgr, electedLeader{}))
	db, err := mgr.Get("locks")
	require.NoError(t, err)
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
	require.NoError(t, awaitLeader(context.Background(), electedLeader{}, testLeaderWait, 1))
}

// flakyProvisioner answers *service.NotLeaderError (leader unknown) for
// the first notLeaderUntil-1 Provision/UpdateSQL/UpdateProtocol calls, then
// delegates to the wrapped provisioner -- standing in for the system
// shard's settling window immediately after awaitLeader confirms a leader
// is locally known (go-qrdb hotfix/cakey-restart, cause 3's pattern
// reproduced at the database drop-fence write: TestDEK_Cluster_* and
// TestRaftSMOverFileStorage_ThreeNodeCluster both failed this way on a real
// multi-node cluster).
type flakyProvisioner struct {
	databaseProvisioner
	calls          int
	notLeaderUntil int
}

func (p *flakyProvisioner) transientRefusal() error {
	p.calls++
	if p.notLeaderUntil > 0 && p.calls < p.notLeaderUntil {
		return &service.NotLeaderError{ShardID: 1}
	}
	return nil
}

func (p *flakyProvisioner) Provision(ctx context.Context, cfg database.DatabaseConfig) (*database.Database, error) {
	if err := p.transientRefusal(); err != nil {
		return nil, err
	}
	return p.databaseProvisioner.Provision(ctx, cfg)
}

func (p *flakyProvisioner) UpdateSQL(name string, cfg *database.SQLConfig) error {
	if err := p.transientRefusal(); err != nil {
		return err
	}
	return p.databaseProvisioner.UpdateSQL(name, cfg)
}

func (p *flakyProvisioner) UpdateProtocol(name string, cfg database.ProtocolConfig) error {
	if err := p.transientRefusal(); err != nil {
		return err
	}
	return p.databaseProvisioner.UpdateProtocol(name, cfg)
}

// TestProvisionDatabases_RetriesTransientNotLeader proves provisionDatabases
// retries a settling-window *service.NotLeaderError on its own write calls
// rather than failing boot the moment awaitLeader's local check is stale.
func TestProvisionDatabases_RetriesTransientNotLeader(t *testing.T) {
	mgr := database.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	flaky := &flakyProvisioner{databaseProvisioner: mgr, notLeaderUntil: 3}

	require.NoError(t, provisionDatabases(context.Background(), provisionFlags(""), flaky, electedLeader{}))
	assert.Equal(t, 3, flaky.calls)
	_, err := mgr.Get("orders")
	require.NoError(t, err)
}

// TestProvisionDatabases_NonLeaderErrorsDoNotRetry proves a non-transient
// failure (anything other than *service.NotLeaderError) is returned on the
// FIRST call, with no retry: waiting cannot fix it.
func TestProvisionDatabases_NonLeaderErrorsDoNotRetry(t *testing.T) {
	boom := errors.New("disk full")
	err := provisionDatabases(context.Background(), provisionFlags(""), failingProvisioner{err: boom}, electedLeader{})
	assert.ErrorIs(t, err, boom)
}

// An invalid configured database is rejected up front, with the typed
// error its validation reports, and a valid configuration passes.
func TestValidateConfiguredDatabases(t *testing.T) {
	require.NoError(t, validateConfiguredDatabases(provisionFlags("")))
	var invalid *database.InvalidBackendError
	err := validateConfiguredDatabases(&startFlags{databases: []qrdbconfig.DatabaseConfig{{Name: "x", Backend: "bogus"}}})
	assert.ErrorAs(t, err, &invalid)
}

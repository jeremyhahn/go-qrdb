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
	"log/slog"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// databaseProvisioner is the slice of *database.Manager boot provisioning
// uses.
type databaseProvisioner interface {
	Get(name string) (*database.Database, error)
	Provision(ctx context.Context, cfg database.DatabaseConfig) (*database.Database, error)
	UpdateProtocol(name string, protocol database.ProtocolConfig) error
	UpdateSQL(name string, sqlCfg *database.SQLConfig) error
}

var _ databaseProvisioner = (*database.Manager)(nil)

// provisionDatabases creates or opens every database the configuration
// lists, then the --sql-database one when a SQL wire protocol is enabled.
//
// It must run after lifecycle.BuildServices has attached the keyspace
// reclaimer to the manager: a name an earlier boot dropped still carries a
// tombstone in the store, and only the reclaimer can clear it. Without one,
// database.Manager refuses the name with *database.ReclaimerRequiredError
// rather than create a database whose every write the tombstone would
// refuse. Provision is idempotent, so a re-run finds every database present.
//
// Provisioning a name marks it live in the system shard's drop-fence
// marker, a Raft write, so it first waits for that shard to know its
// leader: a proposal made before any leader is elected is refused rather
// than queued, and a follower's proposal is forwarded only once it knows
// where to. The wait runs in rounds of leaderWaitBudget(f) and ends only
// with ctx.
//
// awaitLeader only confirms a leader is locally KNOWN, not that the shard
// has finished settling onto it (the same ReadIndex warm-up / election
// churn server.WhileSystemShardSettling exists for elsewhere in this
// daemon's boot): the drop-fence write itself can still race that window
// and come back *service.NotLeaderError with no leader named, immediately
// after awaitLeader returned successfully. Every write below is retried
// through WhileSystemShardSettling for exactly that reason; any other
// error class is returned at once.
func provisionDatabases(ctx context.Context, f *startFlags, dbs databaseProvisioner, node server.LeaderSource) error {
	if err := awaitLeader(ctx, node, leaderWaitBudget(f), shard.ShardKV); err != nil {
		return &DatabaseProvisionError{Step: "wait for leader", Err: err}
	}
	// Every step below is a write and runs on the boot's context, not the
	// attempt's: a cut-off attempt would leave a proposal in flight whose
	// retry races it.
	for _, cfg := range configuredDatabases(f) {
		if err := server.WhileSystemShardSettling(ctx, "provision database "+cfg.Name, func(context.Context) error {
			_, err := dbs.Provision(ctx, cfg)
			return err
		}); err != nil {
			return err
		}
		// The file declares the database's SQL settings: a database that
		// already existed (Provision is idempotent) takes them too, the
		// same way the SQL-database special case below reapplies its
		// protocol config on every boot.
		if cfg.SQL != nil {
			if err := server.WhileSystemShardSettling(ctx, "update SQL settings of database "+cfg.Name, func(context.Context) error {
				return dbs.UpdateSQL(cfg.Name, cfg.SQL)
			}); err != nil {
				return err
			}
		}
	}
	if f.sqlDatabase == "" || (!f.enableMySQL && !f.enablePgSQL) {
		return nil
	}
	_, err := dbs.Get(f.sqlDatabase)
	if err == nil {
		return server.WhileSystemShardSettling(ctx, "update protocol of SQL database "+f.sqlDatabase, func(context.Context) error {
			return dbs.UpdateProtocol(f.sqlDatabase, sqlDatabaseConfig(f).Protocol)
		})
	}
	var nfe *database.NotFoundError
	var dde *database.DroppingError
	switch {
	case errors.As(err, &nfe):
		return server.WhileSystemShardSettling(ctx, "provision SQL database "+f.sqlDatabase, func(context.Context) error {
			_, err := dbs.Provision(ctx, sqlDatabaseConfig(f))
			return err
		})
	case errors.As(err, &dde):
		// The SQL database is fenced by a drop still in flight, most likely
		// one a crash interrupted. Failing boot would leave the node
		// unrecoverable without manual intervention; finishing the drop is a
		// destructive decision boot has no basis to make. The node comes up
		// without --sql-database until the drop is completed or retried
		// through the API.
		slog.Warn("configured SQL database is being dropped; startup is not auto-provisioning it this boot",
			"database", f.sqlDatabase)
		return nil
	default:
		return err
	}
}

// configuredDatabases returns the database configurations the daemon's
// configuration lists. MySQL, PostgreSQL and Etcd carry through to each
// database's Protocol -- previously only the SQL settings did, so a
// `databases:` entry's own `mysql`/`postgresql`/`etcd` block was silently
// dropped at provisioning time regardless of build tags, the same class of
// defect the etcd-specific case of it was named for (go-qrdb review).
func configuredDatabases(f *startFlags) []database.DatabaseConfig {
	cfgs := make([]database.DatabaseConfig, 0, len(f.databases))
	for _, dbCfg := range f.databases {
		cfg := database.DatabaseConfig{
			Name:    dbCfg.Name,
			Backend: database.BackendType(dbCfg.Backend),
			Protocol: database.ProtocolConfig{
				MySQL:      dbCfg.MySQL,
				PostgreSQL: dbCfg.PostgreSQL,
			},
			SQL: databaseSQLConfig(dbCfg.SQL),
		}
		if dbCfg.Etcd != nil {
			cfg.Protocol.Etcd = &database.EtcdConfig{
				Enabled:             dbCfg.Etcd.Enabled,
				ListenAddr:          dbCfg.Etcd.ListenAddr,
				AdvertiseClientURLs: dbCfg.Etcd.AdvertiseClientURLs,
			}
		}
		cfgs = append(cfgs, cfg)
	}
	return cfgs
}

// sqlDatabaseConfig returns the configuration --sql-database is provisioned
// with: a KV database on the state machine's backend, serving the enabled
// SQL wire protocols.
func sqlDatabaseConfig(f *startFlags) database.DatabaseConfig {
	backend := database.BackendPebble
	if f.smType == "memory" || f.smType == "concurrent" {
		backend = database.BackendMemory
	}
	return database.DatabaseConfig{
		Name:    f.sqlDatabase,
		Backend: backend,
		Protocol: database.ProtocolConfig{
			MySQL:      f.enableMySQL,
			PostgreSQL: f.enablePgSQL,
		},
	}
}

// validateConfiguredDatabases rejects an invalid configured database before
// the node starts. Provisioning itself waits for the data plane, and a
// configuration error must not wait for a Raft leader to be reported.
//
// RefuseEtcdNotCompiled runs over EVERY configured entry here, before
// provisionDatabases provisions any of them: on a binary without
// qrdb_etcd, a `databases:` list with its etcd-enabled entry listed
// second must refuse before the first entry is ever created, not after --
// this is the one caller the capability check added for pkg/service's own
// Create/CreateForTenant/UpdateEtcd/UpdateEtcdForTenant could not reach
// (boot-time provisioning calls *database.Manager directly), so it carries
// the same all-before-any discipline cfg.Validate() already has here
// rather than being interleaved into provisionDatabases' per-entry loop.
func validateConfiguredDatabases(f *startFlags) error {
	for _, cfg := range configuredDatabases(f) {
		if err := cfg.Validate(); err != nil {
			return err
		}
		if err := service.RefuseEtcdNotCompiled(cfg.Protocol.Etcd); err != nil {
			return err
		}
	}
	return nil
}

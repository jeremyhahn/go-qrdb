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
// where to.
func provisionDatabases(ctx context.Context, f *startFlags, dbs databaseProvisioner, node leaderReporter) error {
	if err := awaitLeader(ctx, node, shard.ShardKV); err != nil {
		return &DatabaseProvisionError{Step: "wait for leader", Err: err}
	}
	for _, cfg := range configuredDatabases(f) {
		if _, err := dbs.Provision(ctx, cfg); err != nil {
			return err
		}
		// The file declares the database's SQL settings: a database that
		// already existed (Provision is idempotent) takes them too, the
		// same way the SQL-database special case below reapplies its
		// protocol config on every boot.
		if cfg.SQL != nil {
			if err := dbs.UpdateSQL(cfg.Name, cfg.SQL); err != nil {
				return err
			}
		}
	}
	if f.sqlDatabase == "" || !(f.enableMySQL || f.enablePgSQL) {
		return nil
	}
	_, err := dbs.Get(f.sqlDatabase)
	if err == nil {
		return dbs.UpdateProtocol(f.sqlDatabase, sqlDatabaseConfig(f).Protocol)
	}
	var nfe *database.NotFoundError
	var dde *database.DroppingError
	switch {
	case errors.As(err, &nfe):
		_, err := dbs.Provision(ctx, sqlDatabaseConfig(f))
		return err
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
// configuration lists.
func configuredDatabases(f *startFlags) []database.DatabaseConfig {
	cfgs := make([]database.DatabaseConfig, 0, len(f.databases))
	for _, dbCfg := range f.databases {
		cfgs = append(cfgs, database.DatabaseConfig{
			Name:    dbCfg.Name,
			Engine:  database.EngineType(dbCfg.Engine),
			Backend: database.BackendType(dbCfg.Backend),
			SQL:     databaseSQLConfig(dbCfg.SQL),
		})
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
		Engine:  database.EngineKV,
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
func validateConfiguredDatabases(f *startFlags) error {
	for _, cfg := range configuredDatabases(f) {
		if err := cfg.Validate(); err != nil {
			return err
		}
	}
	return nil
}

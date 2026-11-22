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

//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// startSQLDefaultsNode starts a multi-protocol node serving MySQL and
// PostgreSQL wire with --sql-database qrdb (the database waitForMySQL and
// waitForPgSQL connect to) and returns it with its MySQL and PostgreSQL
// ports.
func startSQLDefaultsNode(t *testing.T, smType, extraYAML string) (*multiProtoNode, int, int) {
	t.Helper()
	sqlPorts := allocateTCPPorts(t, 2)
	node := startMultiProtocolNodeWithConfig(t, smType, extraYAML,
		"--enable-mysql", "--mysql-port", fmt.Sprintf("%d", sqlPorts[0]),
		"--enable-pgsql", "--pgsql-port", fmt.Sprintf("%d", sqlPorts[1]),
		"--sql-database", "qrdb")
	return node, sqlPorts[0], sqlPorts[1]
}

// createSQLDatabases runs CREATE DATABASE once over MySQL wire and once over
// PostgreSQL wire and returns the two names.
func createSQLDatabases(t *testing.T, node *multiProtoNode, mysqlPort, pgsqlPort int) (string, string) {
	t.Helper()
	certPath, keyPath := node.ca.boundAdminCert(t, t.TempDir())
	suffix := time.Now().UnixNano()
	mysqlName := fmt.Sprintf("defaults_mysql_%d", suffix)
	pgsqlName := fmt.Sprintf("defaults_pgsql_%d", suffix)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db := waitForMySQL(t, node.ca, certPath, keyPath, mysqlPort, "admin")
	if _, err := db.ExecContext(ctx, "CREATE DATABASE "+mysqlName); err != nil {
		t.Fatalf("MySQL CREATE DATABASE %s: %v", mysqlName, err)
	}
	conn := waitForPgSQL(t, node.ca, certPath, keyPath, pgsqlPort, "admin")
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgsqlName); err != nil {
		t.Fatalf("PostgreSQL CREATE DATABASE %s: %v", pgsqlName, err)
	}
	return mysqlName, pgsqlName
}

// assertBackendEverywhere reads each database's record with `qrdb database
// info` over every transport and holds its backend to want.
func assertBackendEverywhere(t *testing.T, node *multiProtoNode, want, why string, names ...string) {
	t.Helper()
	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			for _, name := range names {
				info := cliDatabaseInfo(t, node, proto, name)
				if info.Backend != want {
					t.Fatalf("[%s] database info %s: Backend = %q, want %q (%s)", proto.name, name, info.Backend, want, why)
				}
			}
		})
	}
}

// TestDatabaseDefaults_CLI_SQLCreatesRecordTheNodeDefault proves a database
// created by SQL CREATE DATABASE (over MySQL and PostgreSQL wire) and the
// --sql-database database are created through the node's database_defaults:
// each records the configured backend, not one SQL or --sm picks, on every
// transport.
func TestDatabaseDefaults_CLI_SQLCreatesRecordTheNodeDefault(t *testing.T) {
	t.Parallel()
	node, mysqlPort, pgsqlPort := startSQLDefaultsNode(t, "", "database_defaults:\n  backend: file\n")
	mysqlName, pgsqlName := createSQLDatabases(t, node, mysqlPort, pgsqlPort)
	assertBackendEverywhere(t, node, "file", "the node's database_defaults backend", "qrdb", mysqlName, pgsqlName)
}

// TestDatabaseDefaults_CLI_SQLCreatesRecordTheRunModeDefault proves that with
// no database_defaults backend the SQL-created and --sql-database databases
// record the raft run mode's default, whatever --sm names.
func TestDatabaseDefaults_CLI_SQLCreatesRecordTheRunModeDefault(t *testing.T) {
	t.Parallel()
	node, mysqlPort, pgsqlPort := startSQLDefaultsNode(t, "memory", "")
	mysqlName, pgsqlName := createSQLDatabases(t, node, mysqlPort, pgsqlPort)
	assertBackendEverywhere(t, node, "raft", "the raft run mode's default; --sm memory picks no database backend", "qrdb", mysqlName, pgsqlName)
}

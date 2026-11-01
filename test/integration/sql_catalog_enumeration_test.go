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
	"database/sql"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// sqlCatalogEnumerationDeadline bounds every poll in this file: the catalog
// entry reaches a follower through Raft replication, so the wait is for one
// commit to apply, not for any timer.
const sqlCatalogEnumerationDeadline = 30 * time.Second

// TestSQL_Cluster_CatalogEnumeration_EveryNode proves that SQL database
// enumeration reads the replicated catalog rather than the node's set of
// opened databases. A database created and enabled for MySQL and PostgreSQL
// through the CLI on node A is listed by `SHOW DATABASES` and by pg_namespace
// on node B, which has run no operation against it; once it is dropped on A,
// both listings on B lose it.
func TestSQL_Cluster_CatalogEnumeration_EveryNode(t *testing.T) {
	c := startThreeNodeClusterWithSQL(t)
	cli := buildCLIBinary(t)

	const nodeA, nodeB = 0, 1
	const dbName = "catalogenum"

	// Connect to B before the database exists, so nothing B does after the
	// create can be what puts the database in its listing.
	mysqlB := c.mysql(t, nodeB)
	pgsqlB := c.pgsql(t, nodeB)

	cliA := func(args ...string) {
		t.Helper()
		full := append([]string{
			"--server", fmt.Sprintf("https://127.0.0.1:%d", c.nodes[nodeA].adminPort),
			"--ca-file", c.ca.CertPath,
			"--cert-file", c.clientCert,
			"--key-file", c.clientKey,
			"--timeout", "20s",
		}, args...)
		if out, err := runCLI(t, cli, full...); err != nil {
			t.Fatalf("qrdb %v on node A: %v\noutput: %s", args, err, out)
		}
	}

	cliA("database", "create", dbName)
	cliA("database", "update-protocol", dbName, "--mysql", "--postgresql")

	awaitListing(t, "MySQL SHOW DATABASES on node B", dbName, true, func() ([]string, error) {
		return mysqlDatabaseNames(mysqlB)
	})
	awaitListing(t, "PostgreSQL pg_namespace on node B", dbName, true, func() ([]string, error) {
		return pgsqlNamespaceNames(pgsqlB)
	})

	cliA("database", "drop", dbName)

	awaitListing(t, "MySQL SHOW DATABASES on node B after drop", dbName, false, func() ([]string, error) {
		return mysqlDatabaseNames(mysqlB)
	})
	awaitListing(t, "PostgreSQL pg_namespace on node B after drop", dbName, false, func() ([]string, error) {
		return pgsqlNamespaceNames(pgsqlB)
	})
}

// awaitListing polls list until its result contains name (want true) or
// lacks it (want false), failing the test with the last listing and error
// when sqlCatalogEnumerationDeadline passes first.
func awaitListing(t *testing.T, what, name string, want bool, list func() ([]string, error)) {
	t.Helper()
	var names []string
	var err error
	if !pollUntil(sqlCatalogEnumerationDeadline, 200*time.Millisecond, func() bool {
		names, err = list()
		return err == nil && slices.Contains(names, name) == want
	}) {
		t.Fatalf("%s: want listed=%t for %q within %s; last listing %v, last error %v",
			what, want, name, sqlCatalogEnumerationDeadline, names, err)
	}
}

// mysqlDatabaseNames returns the result of SHOW DATABASES.
func mysqlDatabaseNames(db *sql.DB) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// pgsqlNamespaceNames returns every nspname in pg_namespace. It runs over the
// simple query protocol so every poll is a fresh Parse: the server executes a
// parameterless statement when it is parsed (ParseHandler.HandleParse), so a
// statement pgx has cached would replay the first poll's rows.
func pgsqlNamespaceNames(conn *pgx.Conn) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := conn.Query(ctx, "SELECT nspname FROM pg_namespace", pgx.QueryExecModeSimpleProtocol)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

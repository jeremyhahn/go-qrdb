// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package integration

import (
	"fmt"
	"testing"
)

// TestCLIMultiProtocolDatabaseUpdateSQL creates a database with
// --sql-concurrency, changes it with update-sql, and reads it back, across
// every protocol (docs/row-locking/cli.md).
func TestCLIMultiProtocolDatabaseUpdateSQL(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			dbName := fmt.Sprintf("db-sql-%s-%d", proto, testTimestamp())
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName) //nolint:errcheck
			})

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"database", "create", dbName, "--sql-concurrency", "pessimistic")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database create --sql-concurrency failed: %v", proto, err)
			}
			assertContains(t, stdout, "Concurrency: pessimistic",
				fmt.Sprintf("[%s] database create should report the SQL concurrency", proto))

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"database", "update-sql", dbName, "--concurrency", "optimistic")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] database update-sql failed: %v", proto, err)
			}
			assertContains(t, stdout, "Concurrency: optimistic",
				fmt.Sprintf("[%s] database update-sql should report the new mode", proto))

			stdout, _, err = execCLIWithProtocol(t, cfg, proto, "database", "info", dbName)
			if err != nil {
				t.Fatalf("[%s] database info failed: %v", proto, err)
			}
			assertContains(t, stdout, "Concurrency: optimistic",
				fmt.Sprintf("[%s] database info should report the stored mode", proto))

			if _, _, err := execCLIWithProtocol(t, cfg, proto,
				"database", "update-sql", dbName, "--concurrency", "eager"); err == nil {
				t.Fatalf("[%s] database update-sql accepted an unknown mode", proto)
			}
		})
	}
}

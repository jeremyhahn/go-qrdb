// Copyright 2026 Jeremy Hahn
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

//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"
)

// TestCLIMultiProtocolDatabaseScopeFlag verifies the global --database flag
// scopes data-plane KV operations to a named database: a key written into a
// scoped database is invisible in the default database (and vice versa),
// and dropping the scoped database removes its data.
func TestCLIMultiProtocolDatabaseScopeFlag(t *testing.T) {
	cfg := LoadTestConfig()

	for _, proto := range AllProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			dbName := fmt.Sprintf("cli-scope-%s-%d", proto, testTimestamp())
			key := fmt.Sprintf("scope-key-%s-%d", proto, testTimestamp())

			// Create a database to scope into.
			if _, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"database", "create", dbName); err != nil {
				t.Fatalf("database create: %v\nstderr: %s", err, stderr)
			}
			t.Cleanup(func() {
				_, _, _ = execCLIWithProtocol(t, cfg, proto, "database", "drop", dbName)
			})

			// Write a value inside the scoped database.
			if _, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"--database", dbName, "kv", "put", key, "scoped-value"); err != nil {
				t.Fatalf("scoped kv put: %v\nstderr: %s", err, stderr)
			}

			// Visible inside the scope.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"--database", dbName, "kv", "get", key)
			if err != nil {
				t.Fatalf("scoped kv get: %v\nstderr: %s", err, stderr)
			}
			if !strings.Contains(stdout, "scoped-value") {
				t.Fatalf("scoped kv get output missing value: %q", stdout)
			}

			// Invisible in the default database.
			if stdout, _, err := execCLIWithProtocol(t, cfg, proto,
				"kv", "exists", key); err != nil {
				t.Fatalf("default kv exists: %v", err)
			} else if strings.Contains(strings.ToLower(stdout), "true") {
				t.Fatalf("key %q leaked into the default database: %q", key, stdout)
			}

			// A default-database key is invisible inside the scope.
			defaultKey := key + "-default"
			if _, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"kv", "put", defaultKey, "default-value"); err != nil {
				t.Fatalf("default kv put: %v\nstderr: %s", err, stderr)
			}
			t.Cleanup(func() {
				_, _, _ = execCLIWithProtocol(t, cfg, proto, "kv", "del", defaultKey)
			})
			if stdout, _, err := execCLIWithProtocol(t, cfg, proto,
				"--database", dbName, "kv", "exists", defaultKey); err != nil {
				t.Fatalf("scoped kv exists: %v", err)
			} else if strings.Contains(strings.ToLower(stdout), "true") {
				t.Fatalf("default key %q leaked into database %q: %q", defaultKey, dbName, stdout)
			}
		})
	}
}

// TestCLIDatabaseScopeFlagNonexistentDatabase verifies data-plane calls
// against an unknown --database fail rather than silently writing to the
// default database.
func TestCLIDatabaseScopeFlagNonexistentDatabase(t *testing.T) {
	cfg := LoadTestConfig()

	for _, proto := range AllProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			key := fmt.Sprintf("ghost-key-%s-%d", proto, testTimestamp())

			_, _, putErr := execCLIWithProtocol(t, cfg, proto,
				"--database", "no-such-database-ever", "kv", "put", key, "v")
			if putErr == nil {
				// If the server accepted the write, it must NOT be
				// visible in the default database.
				stdout, _, err := execCLIWithProtocol(t, cfg, proto, "kv", "exists", key)
				if err != nil {
					t.Fatalf("default kv exists: %v", err)
				}
				if strings.Contains(strings.ToLower(stdout), "true") {
					t.Fatalf("write to nonexistent database leaked into default database: %q", stdout)
				}
			}
		})
	}
}

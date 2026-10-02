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

package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire/dbwiretest"
)

// A database's discovery secrets are write-only: no read of the database
// returns them, only that each one is set.
func TestDatabaseTools_SecretsNeverReadBack(t *testing.T) {
	svc := newRealDatabaseService(t)
	dbwiretest.Seed(t, svc)
	ctx := context.Background()

	reads := []struct {
		tool string
		call func() string
	}{
		{"qrdb_database_get", func() string {
			r, err := handleDatabaseGet(svc)(ctx, buildCallToolRequest(t, "qrdb_database_get", map[string]any{"name": dbwiretest.Name}))
			if err != nil {
				t.Fatal(err)
			}
			return resultText(t, r)
		}},
		{"qrdb_database_list", func() string {
			r, err := handleDatabaseList(svc)(ctx, buildCallToolRequest(t, "qrdb_database_list", nil))
			if err != nil {
				t.Fatal(err)
			}
			return resultText(t, r)
		}},
		{"qrdb_database_update_sql", func() string {
			r, err := handleDatabaseUpdateSQL(svc)(ctx, buildCallToolRequest(t, "qrdb_database_update_sql", map[string]any{
				"name": dbwiretest.Name, "concurrency": "optimistic",
			}))
			if err != nil {
				t.Fatal(err)
			}
			return resultText(t, r)
		}},
	}
	for _, r := range reads {
		dbwiretest.AssertNoSecrets(t, "MCP "+r.tool, []byte(r.call()))
	}

	// The update that sets a secret does not echo it either. The tool sets
	// one discovery method's block, so only that secret is reported.
	r, err := handleDatabaseUpdateRaft(svc)(ctx, buildCallToolRequest(t, "qrdb_database_update_raft", map[string]any{
		"name": dbwiretest.Name, "cluster_mode": "distributed", "replica_id": 1,
		"discovery_method": "static", "static_shared_secret": dbwiretest.StaticSecret,
	}))
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, r)
	if strings.Contains(text, dbwiretest.StaticSecret) || !strings.Contains(text, `"sharedSecretSet":true`) {
		t.Fatalf("MCP qrdb_database_update_raft echoed the secret or did not report it set:\n%s", text)
	}
}

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

package quic

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire/dbwiretest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// A database's discovery secrets are write-only: no read of the database
// returns them, only that each one is set.
func TestHandleDatabaseSecrets_NeverReadBack(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := database.NewManager(logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	svc := service.NewDatabaseService(mgr)
	dbwiretest.Seed(t, svc)
	srv := NewServer(nil, nil, "localhost:0", WithDatabaseService(svc))
	ctx := context.Background()

	payload := func(v any) []byte {
		out, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	reads := []struct {
		what    string
		handler func(context.Context, []byte) ([]byte, error)
		payload []byte
	}{
		{"database.get", srv.handleDatabaseGet, payload(map[string]string{"name": dbwiretest.Name})},
		{"database.list", srv.handleDatabaseList, nil},
		{"database.update_sql", srv.handleDatabaseUpdateSQL, payload(map[string]any{
			"name": dbwiretest.Name, "sql": map[string]string{"concurrency": "optimistic"},
		})},
		// The update that sets the secrets does not echo them either.
		{"database.update_raft", srv.handleDatabaseUpdateRaft, payload(map[string]any{
			"name": dbwiretest.Name, "raft": dbwiretest.RaftConfig(),
		})},
	}
	for _, r := range reads {
		t.Run(r.what, func(t *testing.T) {
			resp, err := r.handler(ctx, r.payload)
			if err != nil {
				t.Fatalf("%s: %v", r.what, err)
			}
			dbwiretest.AssertNoSecrets(t, "QUIC "+r.what, resp)
		})
	}
}

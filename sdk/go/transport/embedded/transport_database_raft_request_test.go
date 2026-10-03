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

package embedded

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire/dbwiretest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Every field of the SDK's Raft request -- discovery blocks and their secrets
// included -- reaches the database in-process.
func TestEmbeddedDatabase_RaftRequestReachesTheService(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := database.NewManager(logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	svc := service.NewDatabaseService(mgr)
	dbwiretest.Seed(t, svc)
	tr := newExtTransport(t, WithDatabaseService(svc))

	info, err := tr.UpdateDatabaseRaft(context.Background(), transport.UpdateDatabaseRaftRequest{
		Name: dbwiretest.Name, Raft: dbwiretest.FullRaftRequest(),
	})
	if err != nil {
		t.Fatalf("UpdateDatabaseRaft: %v", err)
	}
	if info.Raft == nil || info.Raft.Static == nil || !info.Raft.Static.SharedSecretSet {
		t.Fatalf("response Raft = %+v, want the static secret reported as set", info.Raft)
	}
	stored, err := svc.Get(dbwiretest.Name)
	if err != nil {
		t.Fatal(err)
	}
	dbwiretest.AssertRaftConfig(t, "embedded", stored.Raft)
}

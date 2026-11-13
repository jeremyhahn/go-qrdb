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

package unix

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire/dbwiretest"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// A database's discovery secrets are write-only over the Unix socket: the
// server's response carries none of them, only that each one is set, and the
// SDK reports the same.
func TestUnixRT_DatabaseSecretsNeverReadBack(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := databasetest.NewManager(logger, t.TempDir())
	dbwiretest.ClaimShards(t, mgr)
	t.Cleanup(func() { _ = mgr.Close() })
	svc := service.NewDatabaseService(mgr)
	dbwiretest.Seed(t, svc)
	tr, cleanup := startAuthServer(t, unixserver.WithDatabaseService(svc))
	defer cleanup()
	ctx := context.Background()

	// What the server sends.
	got, err := tr.client.DatabaseGet(ctx, &pb.DatabaseGetRequest{Name: dbwiretest.Name})
	if err != nil {
		t.Fatalf("DatabaseGet: %v", err)
	}
	wire, err := protojson.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	dbwiretest.AssertNoSecrets(t, "Unix DatabaseGet (wire)", wire)
	list, err := tr.client.DatabaseList(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatalf("DatabaseList: %v", err)
	}
	if wire, err = protojson.Marshal(list); err != nil {
		t.Fatal(err)
	}
	dbwiretest.AssertNoSecrets(t, "Unix DatabaseList (wire)", wire)

	// What the SDK returns.
	info, err := tr.GetDatabase(ctx, dbwiretest.Name)
	if err != nil {
		t.Fatalf("GetDatabase: %v", err)
	}
	sdk, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	dbwiretest.AssertNoSecrets(t, "Unix GetDatabase (SDK)", sdk)
}

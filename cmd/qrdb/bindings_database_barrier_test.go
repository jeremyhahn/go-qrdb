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

package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// barrierDatabaseService is the real DatabaseService with a real per-database
// barrier manager over a none-strategy system barrier, wired as the daemon
// wires it.
func barrierDatabaseService(t *testing.T) *service.DatabaseService {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := database.NewManager(logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })

	sys, err := seal.NewStorageBarrier(logger, seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "root-key", DeploymentID: 1}, seal.NewNoneStrategy())
	if err != nil {
		t.Fatalf("system barrier: %v", err)
	}
	if err := sys.Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("initialize system barrier: %v", err)
	}
	bm, err := database.NewDatabaseBarrierManager(logger, sys)
	if err != nil {
		t.Fatalf("database barrier manager: %v", err)
	}
	mgr.SetBarrierManager(bm)
	svc, ok := service.NewDatabaseService(mgr, service.WithBarrierManager(bm)).(*service.DatabaseService)
	if !ok {
		t.Fatal("NewDatabaseService did not return *service.DatabaseService")
	}
	return svc
}

// The desktop wizard's create request -- barrier enabled, with the passphrase
// it collected -- reaches the server through the binding and initialises the
// database's barrier under that passphrase: the database is unsealed after
// create, a reseal refuses a wrong passphrase, and the one sent unseals it.
// The response does not carry the passphrase.
func TestDatabaseBinding_CreateWithBarrierInitialisesIt(t *testing.T) {
	const passphrase = "desktop-wizard-passphrase-41f9"
	svc := barrierDatabaseService(t)
	b := NewDatabaseBinding(testLogger(t), newTestConnectionBinding(t, newMockKVService(), newMockAdminService(),
		embedded.WithDatabaseService(svc)))

	info, err := b.Create(&transport.CreateDatabaseRequest{
		Name:    "vaulted",
		Engine:  "kv",
		Backend: "pebble",
		DataDir: t.TempDir(),
		Barrier: &transport.DatabaseBarrierConfig{Enabled: true, Mode: "passphrase", Secret: passphrase},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.Barrier == nil || !info.Barrier.Enabled || info.Barrier.Mode != "passphrase" {
		t.Fatalf("created barrier = %+v, want enabled passphrase", info.Barrier)
	}
	if out, _ := json.Marshal(info); strings.Contains(string(out), passphrase) {
		t.Fatalf("the create response carries the passphrase: %s", out)
	}

	ctx := context.Background()
	status, err := svc.DatabaseBarrierStatus(ctx, "vaulted")
	if err != nil {
		t.Fatalf("DatabaseBarrierStatus: %v", err)
	}
	if status.Sealed {
		t.Fatal("the barrier is sealed right after create; the create did not initialise it")
	}

	if err := svc.SealDatabase(ctx, "vaulted"); err != nil {
		t.Fatalf("SealDatabase: %v", err)
	}
	if err := svc.UnsealDatabase(ctx, "vaulted", seal.MustCredentials(context.Background(), []byte("not-the-passphrase"))); err == nil {
		t.Fatal("a wrong passphrase unsealed the database")
	}
	if err := svc.UnsealDatabase(ctx, "vaulted", seal.MustCredentials(context.Background(), []byte(passphrase))); err != nil {
		t.Fatalf("the passphrase sent at create does not unseal the database: %v", err)
	}
}

// Without a barrier secret the barrier cannot be initialised, so the create
// is refused rather than leaving a barrier nobody can unseal.
func TestDatabaseBinding_CreateWithBarrierNeedsTheSecret(t *testing.T) {
	svc := barrierDatabaseService(t)
	b := NewDatabaseBinding(testLogger(t), newTestConnectionBinding(t, newMockKVService(), newMockAdminService(),
		embedded.WithDatabaseService(svc)))

	_, err := b.Create(&transport.CreateDatabaseRequest{
		Name:    "unkeyed",
		Engine:  "kv",
		Backend: "pebble",
		DataDir: t.TempDir(),
		Barrier: &transport.DatabaseBarrierConfig{Enabled: true, Mode: "passphrase"},
	})
	if err == nil {
		t.Fatal("a barrier create with no secret succeeded")
	}
	if _, getErr := svc.Get("unkeyed"); getErr == nil {
		t.Fatal("the refused create left the database behind")
	}
}

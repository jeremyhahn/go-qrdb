// Copyright (c) 2026 Jeremy Hahn
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

package node

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// openSealStore opens the node's seal Pebble store under dataDir the way
// buildNodeSealSubsystem does. It fails the test if the store is still held
// open by someone else.
func openSealStore(t *testing.T, dataDir string) (*pebbleStorage.PebbleStorage, seal.StorageBackend) {
	t.Helper()
	cfg := pebbleStorage.DefaultConfig(filepath.Join(dataDir, "seal"))
	cfg.SyncWrites = true
	store, err := pebbleStorage.New(cfg)
	if err != nil {
		t.Fatalf("open the seal store: %v", err)
	}
	return store, seal.NewStorageAdapter(store)
}

func recoverTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestBuildNodeSealSubsystem_RecoveryRefusalStopsStartupAndRetries: a
// committed system encryption migration whose staging root key is missing
// stops the embedded node's startup with the typed refusal, closes the seal
// store and keeps the journal; once the staging key is back, the next start
// rolls the migration forward and clears the journal (Codex round-2 P1 on
// #1687).
func TestBuildNodeSealSubsystem_RecoveryRefusalStopsStartupAndRetries(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	store, backend := openSealStore(t, dataDir)
	none, err := seal.NewStorageBarrier(recoverTestLogger(), backend,
		seal.BarrierConfig{RootKeyPath: "root-key"}, seal.NewNoneStrategy())
	if err != nil {
		t.Fatalf("none barrier: %v", err)
	}
	if err := none.Initialize(ctx, seal.Credentials{}); err != nil {
		t.Fatalf("init none barrier: %v", err)
	}
	j := &seal.MigrationJournal{
		SourceStrategy: seal.StrategyNone, TargetStrategy: seal.StrategyPassphrase,
		Mechanism: seal.MechanismSwap, Phase: seal.PhaseCommitting,
		StagingRootKeyPath: "root-key.migrating",
	}
	jblob, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Put(ctx, "root-key.migration", jblob); err != nil {
		t.Fatalf("write journal: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	cfg := qrdbconfig.DefaultConfig()
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = string(seal.StrategyNone)
	cfg.Seal.RootKeyPath = "root-key"

	sub, err := buildNodeSealSubsystem(ctx, cfg, dataDir, recoverTestLogger(), nil, server.CryptoOptions{})
	if !errors.Is(err, database.ErrMigrationStagingRootUnreadable) {
		if sub != nil {
			_ = sub.close()
		}
		t.Fatalf("build with an unreadable staging root key = %v, want ErrMigrationStagingRootUnreadable", err)
	}

	// The refusal closed the seal store (else this open fails on its lock)
	// and kept the journal for the next start.
	store, backend = openSealStore(t, dataDir)
	if got, lerr := seal.LoadMigrationJournalFrom(ctx, backend, "root-key"); lerr != nil || got == nil {
		t.Fatalf("journal after the refusal = %+v (err %v), want it kept", got, lerr)
	}
	keyed, err := seal.NewStorageBarrier(recoverTestLogger(), backend,
		seal.BarrierConfig{RootKeyPath: "root-key.migrating"}, seal.NewDeferredSoftwareStrategy())
	if err != nil {
		t.Fatalf("keyed barrier: %v", err)
	}
	if err := keyed.Initialize(ctx, seal.MustCredentials(ctx, []byte("recover-staging"))); err != nil {
		t.Fatalf("init keyed staging barrier: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	sub, err = buildNodeSealSubsystem(ctx, cfg, dataDir, recoverTestLogger(), nil, server.CryptoOptions{})
	if err != nil {
		t.Fatalf("build after the staging root key is back: %v", err)
	}
	if err := sub.close(); err != nil {
		t.Fatal(err)
	}

	store, backend = openSealStore(t, dataDir)
	defer func() { _ = store.Close() }()
	if got, lerr := seal.LoadMigrationJournalFrom(ctx, backend, "root-key"); lerr != nil || got != nil {
		t.Fatalf("journal after the retried recovery = %+v (err %v), want it cleared", got, lerr)
	}
}

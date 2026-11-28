package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// migrationDatabaseCanary is a value written in plaintext to a database's
// own store; after the migration no file of that store may still hold it.
var migrationDatabaseCanary = []byte("qrdb-system-migration-database-canary")

// newMigrationDatabase builds the boot database manager the daemon builds,
// with its binding factory, and creates a prefix tenant's pebble database
// in a store of its own holding the canary.
func newMigrationDatabase(t *testing.T, dataDir string, raftMode bool) (*database.Manager, *int) {
	t.Helper()
	m := newBootDatabaseManager(dataDir, database.Defaults{})
	if _, err := installBindingFactory(m, dataDir, database.Defaults{}, nil, qrdbconfig.StorageConfig{},
		func() tenant.TenantServicer { return prefixTenantService{} },
		func() *seal.BarrierRegistry { return nil }, raftMode); err != nil {
		t.Fatalf("installBindingFactory: %v", err)
	}
	cat, err := database.NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	if err != nil {
		t.Fatalf("NewKVCatalog: %v", err)
	}
	m.SetCatalog(cat)
	db, err := m.Create(database.DatabaseConfig{TenantID: "acme", Name: "orders", Backend: database.BackendPebble})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := db.Backend().Put(context.Background(), []byte("k"), migrationDatabaseCanary); err != nil {
		t.Fatalf("Put: %v", err)
	}
	closes := new(int)
	t.Cleanup(func() { _ = m.Close() })
	return m, closes
}

// storesHoldingCanary names the files under root that still hold the canary.
func storesHoldingCanary(t *testing.T, root string) []string {
	t.Helper()
	var hits []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if bytes.Contains(data, migrationDatabaseCanary) {
			hits = append(hits, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return hits
}

// TestHandleSystemMigration_ReencryptsDatabaseStores pins that the daemon's
// system encryption migration re-encrypts every database the node barrier
// encrypts in a store of its own, after closing the databases to release
// their store locks. A skipped store keeps its plaintext.
func TestHandleSystemMigration_ReencryptsDatabaseStores(t *testing.T) {
	for _, raftMode := range []bool{true, false} {
		t.Run(fmt.Sprintf("raftMode=%v", raftMode), func(t *testing.T) {
			dataDir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dataDir, "wal"), 0o700); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			m, closes := newMigrationDatabase(t, dataDir, raftMode)
			dbRoot := filepath.Join(dataDir, "tenants", "acme")
			closeDatabases := func() error { *closes++; return m.Close() }
			if hits := storesHoldingCanary(t, dbRoot); len(hits) == 0 {
				t.Fatalf("no store under %s holds the canary before the migration", dbRoot)
			}

			source := newRecodeSourceBarrier(t)
			req := &systemMigrationRequest{opts: validSystemMigrateOpts(), done: make(chan error, 1)}
			f := &startFlags{dataDir: dataDir, smType: "pebble"}
			reboot, err := handleSystemMigration(req, f, nil, nil, nil, nil, nil, nil, nil,
				&sealSubsystem{SealBarrier: source}, migrationDatabases{manager: m, close: closeDatabases})
			if err != nil || !reboot {
				t.Fatalf("handleSystemMigration = %v, %v; want a reboot", reboot, err)
			}
			if doneErr := <-req.done; doneErr != nil {
				t.Fatalf("req.done = %v, want nil", doneErr)
			}
			if *closes != 1 {
				t.Fatalf("database close calls = %d, want 1", *closes)
			}
			if hits := storesHoldingCanary(t, dbRoot); len(hits) != 0 {
				t.Fatalf("database store files still in plaintext after the migration: %v", hits)
			}
		})
	}
}

// TestHandleSystemMigration_RefusesUnmovableDatabaseStore pins that a
// database store the migration cannot move refuses it before the data plane
// is torn down.
func TestHandleSystemMigration_RefusesUnmovableDatabaseStore(t *testing.T) {
	dataDir := t.TempDir()
	m, closes := newMigrationDatabase(t, dataDir, true)
	source := newRecodeSourceBarrier(t)
	req := &systemMigrationRequest{opts: validSystemMigrateOpts(), done: make(chan error, 1)}
	f := &startFlags{dataDir: dataDir, smType: "pebble",
		storageConfig: qrdbconfig.StorageConfig{WALDir: filepath.Join(dataDir, "external-wal")}}
	bootCtx, bootCancel := context.WithCancel(context.Background())
	defer bootCancel()
	reboot, err := handleSystemMigration(req, f, nil, nil, nil, nil, nil, nil, bootCancel,
		&sealSubsystem{SealBarrier: source}, migrationDatabases{manager: m, close: func() error { *closes++; return nil }})
	var unsupported *database.StoreMigrationUnsupportedError
	if reboot || !errors.As(err, &unsupported) {
		t.Fatalf("handleSystemMigration = %v, %v; want *StoreMigrationUnsupportedError, no reboot", reboot, err)
	}
	if doneErr := <-req.done; !errors.Is(doneErr, err) {
		t.Fatalf("req.done = %v, want %v", doneErr, err)
	}
	if bootCtx.Err() != nil || *closes != 0 {
		t.Fatalf("data plane torn down before the refusal (boot ctx %v, closes %d)", bootCtx.Err(), *closes)
	}
	if got := source.ActiveStrategy(); got != seal.StrategyNone {
		t.Fatalf("active strategy = %q, want %q", got, seal.StrategyNone)
	}
}

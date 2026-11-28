package database

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// TestNewDatabaseID_IsOpaqueAndNeverReused: a database id is 128 random
// bits, hex encoded, and no two allocations collide.
func TestNewDatabaseID_IsOpaqueAndNeverReused(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id, err := NewDatabaseID()
		if err != nil {
			t.Fatalf("NewDatabaseID: %v", err)
		}
		raw, err := hex.DecodeString(id)
		if err != nil || len(raw) != 16 {
			t.Fatalf("id %q is not 128 bits of hex", id)
		}
		if seen[id] {
			t.Fatalf("id %q allocated twice", id)
		}
		seen[id] = true
	}
}

// TestKVCatalog_CreateRefusesARecordWithoutAnID: every record outside the
// catalog names the database by its id, so a record with none is refused.
func TestKVCatalog_CreateRefusesARecordWithoutAnID(t *testing.T) {
	cat, _ := newTestCatalog(t, nil)
	err := cat.Create(context.Background(), DatabaseMeta{TenantID: "acme", Name: "orders", Backend: BackendMemory})
	var required *DatabaseIDRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("Create without an id: err = %v, want *DatabaseIDRequiredError", err)
	}
	if !strings.Contains(required.Error(), "orders") {
		t.Fatalf("DatabaseIDRequiredError = %q", required.Error())
	}
	cause := errors.New("cause")
	if idErr := (&DatabaseIDError{Err: cause}); !errors.Is(idErr, cause) || !strings.Contains(idErr.Error(), "database id") {
		t.Fatalf("DatabaseIDError = %q", idErr.Error())
	}
}

// TestManager_Create_RecordsTheDatabaseIDInTheCatalog: create allocates the
// id and stores it in the catalog record; the database and its info carry
// the same id; a database dropped and created again under the same name
// gets a different one.
func TestManager_Create_RecordsTheDatabaseIDInTheCatalog(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mgr := newCatalogManager(nil, dir)
	defer func() { _ = mgr.Close() }()

	db, err := mgr.Create(DatabaseConfig{Name: "orders", Backend: BackendMemory})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	meta, err := testCatalogFor(dir).Get(ctx, "", "orders")
	if err != nil {
		t.Fatalf("catalog Get: %v", err)
	}
	if meta.ID == "" || meta.ID != db.ID() || db.Info().ID != meta.ID {
		t.Fatalf("catalog id %q, database id %q, info id %q: want one non-empty id", meta.ID, db.ID(), db.Info().ID)
	}
	if strings.Contains(meta.ID, "orders") {
		t.Fatalf("id %q carries the database name", meta.ID)
	}

	if err := mgr.Drop(ctx, "orders"); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	again, err := mgr.Create(DatabaseConfig{Name: "orders", Backend: BackendMemory})
	if err != nil {
		t.Fatalf("Create again: %v", err)
	}
	if again.ID() == "" || again.ID() == meta.ID {
		t.Fatalf("re-created database id %q, first id %q: an id must never be reused", again.ID(), meta.ID)
	}
}

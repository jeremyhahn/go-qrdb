package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/database/objstoredb"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestBuildDedicatedStoreServices_ObjstoreIncrementSurvivesReopen: an
// IDStrategyIncrement collection in an objstore database keeps its sequence
// in the database's own store, so after the database is closed and reopened
// the next insert continues from the last ID instead of restarting a
// process-local counter at 1 and colliding with the stored document.
func TestBuildDedicatedStoreServices_ObjstoreIncrementSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	cfg := database.DatabaseConfig{
		Name:    "blobs",
		Backend: database.BackendObjstore,
		Objstore: &database.DatabaseObjstoreConfig{
			Provider: "local",
			Settings: map[string]string{"path": filepath.Join(t.TempDir(), "objects")},
		},
	}
	newMgr := func() *database.Manager {
		mgr := databasetest.NewManager(nil, dataDir, database.WithBackendFactory(objstoredb.NewBackendFactory()))
		t.Cleanup(func() { _ = mgr.Close() })
		return mgr
	}
	insert := func(coll service.CollectionServicer) uint64 {
		t.Helper()
		resp, err := coll.InsertDocument(ctx, &service.InsertDocumentRequest{
			Database: "blobs", Collection: "docs", Document: map[string]interface{}{"v": "x"},
		})
		if err != nil {
			t.Fatalf("InsertDocument: %v", err)
		}
		return resp.ID
	}

	first := newMgr()
	db, err := first.Create(cfg)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	coll := buildDedicatedStoreServices(db).Collection
	if _, err := coll.CreateCollection(ctx, &service.CreateCollectionRequest{
		Database: "blobs", Name: "docs", IDStrategy: service.IDStrategyIncrement,
	}); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	firstID := insert(coll)
	secondID := insert(coll)
	if secondID != firstID+1 {
		t.Fatalf("second ID = %d, want %d", secondID, firstID+1)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second := newMgr()
	reopened, err := second.Open(ctx, "", "blobs")
	if err != nil {
		t.Fatalf("Open after close: %v", err)
	}
	coll = buildDedicatedStoreServices(reopened).Collection
	if got := insert(coll); got != secondID+1 {
		t.Fatalf("ID after reopen = %d, want %d", got, secondID+1)
	}
}

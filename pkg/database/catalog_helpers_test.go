package database

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// testCatalogs holds one in-memory catalog per data directory, so a second
// Manager over the same data directory -- a restart -- finds the records the
// first one wrote, as a node finds them in the replicated store.
var testCatalogs sync.Map // filepath.Clean(dataDir) -> *KVCatalog

// testCatalogFor returns the in-memory catalog shared by every test Manager
// over dataDir.
func testCatalogFor(dataDir string) *KVCatalog {
	key := filepath.Clean(dataDir)
	if c, ok := testCatalogs.Load(key); ok {
		return c.(*KVCatalog) //nolint:errcheck // the map only ever holds *KVCatalog
	}
	cat, err := NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	if err != nil {
		panic(err) // a non-nil system store is the only precondition
	}
	c, _ := testCatalogs.LoadOrStore(key, cat)
	return c.(*KVCatalog) //nolint:errcheck // the map only ever holds *KVCatalog
}

// withTestID gives a record a database id, as Manager.create does, so a test
// can write it to the catalog directly.
func withTestID(meta DatabaseMeta) DatabaseMeta {
	meta.ID = "test-id"
	return meta
}

// catalogRecord returns the record m's catalog holds for tenantID's database
// name, failing the test when there is none.
func catalogRecord(t *testing.T, m *Manager, tenantID, name string) DatabaseMeta {
	t.Helper()
	cat, err := m.catalog("test")
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	meta, err := cat.Get(context.Background(), tenantID, name)
	if err != nil {
		t.Fatalf("catalog Get(%q, %q): %v", tenantID, name, err)
	}
	return meta
}

// newCatalogManager is NewManager with the data directory's shared in-memory
// catalog attached, the way lifecycle.BuildServices attaches the replicated
// one.
func newCatalogManager(logger *slog.Logger, dataDir string, opts ...ManagerOption) *Manager {
	m := NewManager(logger, dataDir, opts...)
	m.SetCatalog(testCatalogFor(dataDir))
	return m
}

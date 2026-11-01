// Package databasetest builds database Managers for tests outside
// pkg/database. A Manager keeps its database records in the replicated
// catalog that lifecycle.BuildServices attaches; a test with no cluster
// attaches an in-memory one instead, through NewManager.
package databasetest

import (
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// catalogs holds one in-memory catalog per data directory, so a second
// Manager over the same data directory -- a restart -- finds the records the
// first one wrote, as a node finds them in the replicated store.
var catalogs sync.Map // filepath.Clean(dataDir) -> *database.KVCatalog

// Catalog returns the in-memory catalog every Manager NewManager builds over
// dataDir shares.
func Catalog(dataDir string) *database.KVCatalog {
	key := filepath.Clean(dataDir)
	if c, ok := catalogs.Load(key); ok {
		return c.(*database.KVCatalog) //nolint:errcheck // the map only ever holds *database.KVCatalog
	}
	cat, err := database.NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	if err != nil {
		panic(err) // a non-nil system store is the only precondition
	}
	c, _ := catalogs.LoadOrStore(key, cat)
	return c.(*database.KVCatalog) //nolint:errcheck // the map only ever holds *database.KVCatalog
}

// NewManager is database.NewManager with dataDir's in-memory catalog
// attached, the way lifecycle.BuildServices attaches the replicated one.
func NewManager(logger *slog.Logger, dataDir string, opts ...database.ManagerOption) *database.Manager {
	m := database.NewManager(logger, dataDir, opts...)
	m.SetCatalog(Catalog(dataDir))
	return m
}

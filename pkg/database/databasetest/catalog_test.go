package databasetest

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

func TestCatalog_OnePerDataDirectory(t *testing.T) {
	dir := t.TempDir()
	if Catalog(dir) != Catalog(dir+"/.") {
		t.Fatal("two spellings of one data directory got two catalogs")
	}
	if Catalog(dir) == Catalog(t.TempDir()) {
		t.Fatal("two data directories share one catalog")
	}
}

func TestNewManager_RestartFindsTheDatabase(t *testing.T) {
	dir := t.TempDir()
	cfg := database.DatabaseConfig{Name: "orders", Backend: database.BackendMemory}

	m := NewManager(nil, dir)
	if _, err := m.Create(cfg); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	restarted := NewManager(nil, dir)
	defer func() { _ = restarted.Close() }()
	if _, err := restarted.Open(context.Background(), "", "orders"); err != nil {
		t.Fatalf("Open after a restart: %v", err)
	}
}

func TestNewManager_FreshDirectoryHasNoDatabases(t *testing.T) {
	m := NewManager(nil, t.TempDir())
	defer func() { _ = m.Close() }()
	var nf *database.NotFoundError
	if _, err := m.Open(context.Background(), "", "orders"); !errors.As(err, &nf) {
		t.Fatalf("Open in a fresh directory = %v, want *NotFoundError", err)
	}
}

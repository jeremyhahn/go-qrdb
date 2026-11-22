package database

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// rootedBackend is a backend that owns a store root of its own, the way a
// materialized binding does (DataRootOwner).
type rootedBackend struct {
	Backend
	root *string
}

func (b *rootedBackend) DataRoot() string { return *b.root }

// rootedFactory opens an embedded backend and gives it the binding root a
// materialized database gets: its explicit data_dir, else
// <node>/tenants/<tenant>/databases/<name>. roots records each database's
// root so a test can move one.
type rootedFactory struct {
	EmbeddedBackendFactory
	node  string
	roots map[string]*string
}

func (f *rootedFactory) Create(cfg DatabaseConfig, dataDir string) (Backend, error) {
	be, err := f.EmbeddedBackendFactory.Create(cfg, dataDir)
	if err != nil {
		return nil, err
	}
	tenantDir := cfg.TenantID
	if tenantDir == "" {
		tenantDir = "system"
	}
	root := filepath.Join(f.node, "tenants", tenantDir, "databases", cfg.Name)
	if cfg.DataDir != "" {
		root = cfg.DataDir
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, errors.Join(err, be.Close())
	}
	r := root
	f.roots[cfg.Name] = &r
	return &rootedBackend{Backend: be, root: &r}, nil
}

func newRootedManager(t *testing.T) (*Manager, *rootedFactory, string) {
	t.Helper()
	dir := t.TempDir()
	f := &rootedFactory{node: dir, roots: map[string]*string{}}
	m := newCatalogManager(slog.Default(), dir, WithBackendFactory(f))
	t.Cleanup(func() { _ = m.Close() })
	return m, f, dir
}

// TestManager_DataDirOverBindingRootsRefused proves a data_dir that holds the
// default binding roots of the system tenant (<node>/tenants/system) is
// refused in either creation order, so its drop can never delete them.
func TestManager_DataDirOverBindingRootsRefused(t *testing.T) {
	t.Run("data_dir after a default-root database", func(t *testing.T) {
		m, _, dir := newRootedManager(t)
		if _, err := m.Create(DatabaseConfig{Name: "a", Backend: BackendMemory}); err != nil {
			t.Fatalf("Create a: %v", err)
		}
		_, err := m.Create(DatabaseConfig{Name: "evil", Backend: BackendMemory, DataDir: filepath.Join(dir, "tenants", "system")})
		var conflict *DataDirConflictError
		if !errors.As(err, &conflict) || conflict.With != "a" {
			t.Fatalf("Create evil: err = %v, want *DataDirConflictError with a", err)
		}
	})
	t.Run("default-root database after the data_dir", func(t *testing.T) {
		m, _, dir := newRootedManager(t)
		if _, err := m.Create(DatabaseConfig{Name: "evil", Backend: BackendMemory, DataDir: filepath.Join(dir, "tenants", "system")}); err != nil {
			t.Fatalf("Create evil: %v", err)
		}
		_, err := m.Create(DatabaseConfig{Name: "a", Backend: BackendMemory})
		var conflict *DataDirConflictError
		if !errors.As(err, &conflict) || conflict.With != "evil" {
			t.Fatalf("Create a: err = %v, want *DataDirConflictError with evil", err)
		}
		if _, err := m.Get("a"); err == nil {
			t.Fatal("a refused database is registered")
		}
	})
}

// TestManager_DropNeverRemovesAnotherRoot proves Drop refuses, removing
// nothing, a database whose store root contains another database's root.
func TestManager_DropNeverRemovesAnotherRoot(t *testing.T) {
	m, f, _ := newRootedManager(t)
	for _, name := range []string{"a", "b"} {
		if _, err := m.Create(DatabaseConfig{Name: name, Backend: BackendMemory}); err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
	}
	aRoot := *f.roots["a"]
	*f.roots["b"] = filepath.Dir(aRoot)
	err := m.Drop(context.Background(), "b")
	var dropErr *DropError
	if !errors.As(err, &dropErr) {
		t.Fatalf("Drop b: err = %v, want *DropError", err)
	}
	var conflict *DataDirConflictError
	if !errors.As(err, &conflict) || conflict.With != "a" {
		t.Fatalf("Drop b: err = %v, want a *DataDirConflictError with a", err)
	}
	if _, err := os.Stat(aRoot); err != nil {
		t.Fatalf("a's root after the refused drop: %v", err)
	}
	if _, err := m.Get("b"); err != nil {
		t.Fatalf("b after the refused drop: %v", err)
	}
}

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
	fail  map[string]error
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

// RecordRoot is the root Create gives cfg's database (RecordRootResolver),
// the one recorded in roots once it was created; fail makes a database's
// root unresolvable.
func (f *rootedFactory) RecordRoot(cfg DatabaseConfig) (string, error) {
	if err := f.fail[cfg.Name]; err != nil {
		return "", err
	}
	if r, ok := f.roots[cfg.Name]; ok {
		return *r, nil
	}
	if cfg.DataDir != "" {
		return cfg.DataDir, nil
	}
	tenantDir := cfg.TenantID
	if tenantDir == "" {
		tenantDir = "system"
	}
	return filepath.Join(f.node, "tenants", tenantDir, "databases", cfg.Name), nil
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

// recordUnopened creates tenant's database name and then forgets it on this
// node the way OpenAll leaves a record it does not open: the catalog keeps
// the record, and the store root stays on disk. It returns the root.
func recordUnopened(t *testing.T, m *Manager, f *rootedFactory, tenantID, name string) string {
	t.Helper()
	db, err := m.Create(DatabaseConfig{TenantID: tenantID, Name: name, Backend: BackendMemory})
	if err != nil {
		t.Fatalf("Create %s/%s: %v", tenantID, name, err)
	}
	m.databases.Delete(keyFor(tenantID, name))
	if err := db.Close(); err != nil {
		t.Fatalf("Close %s/%s: %v", tenantID, name, err)
	}
	root := *f.roots[name]
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("%s/%s root: %v", tenantID, name, err)
	}
	return root
}

// TestManager_RootsCheckUnopenedRecords proves a create and a drop check
// every catalog record's root, not only the databases open on this node: a
// system-tenant data_dir over a tenant's unopened databases is refused, a
// drop whose root holds one removes nothing, and a root that cannot be
// resolved fails closed.
func TestManager_RootsCheckUnopenedRecords(t *testing.T) {
	t.Run("create over an unopened tenant's root", func(t *testing.T) {
		m, f, dir := newRootedManager(t)
		appRoot := recordUnopened(t, m, f, "acme", "app")
		_, err := m.Create(DatabaseConfig{Name: "evil", Backend: BackendMemory, DataDir: filepath.Join(dir, "tenants", "acme")})
		var conflict *DataDirConflictError
		if !errors.As(err, &conflict) || conflict.With != "app" {
			t.Fatalf("Create evil: err = %v, want *DataDirConflictError with app", err)
		}
		if _, err := m.Get("evil"); err == nil {
			t.Fatal("the refused database is registered")
		}
		if _, err := os.Stat(appRoot); err != nil {
			t.Fatalf("app's root after the refused create: %v", err)
		}
	})
	t.Run("drop over an unopened record", func(t *testing.T) {
		m, f, dir := newRootedManager(t)
		appRoot := recordUnopened(t, m, f, "acme", "app")
		if _, err := m.Create(DatabaseConfig{Name: "b", Backend: BackendMemory}); err != nil {
			t.Fatalf("Create b: %v", err)
		}
		*f.roots["b"] = filepath.Join(dir, "tenants", "acme")
		err := m.Drop(context.Background(), "b")
		var dropErr *DropError
		var conflict *DataDirConflictError
		if !errors.As(err, &dropErr) || !errors.As(err, &conflict) || conflict.With != "app" {
			t.Fatalf("Drop b: err = %v, want a *DropError with a *DataDirConflictError with app", err)
		}
		if _, err := os.Stat(appRoot); err != nil {
			t.Fatalf("app's root after the refused drop: %v", err)
		}
		if _, err := m.Get("b"); err != nil {
			t.Fatalf("b after the refused drop: %v", err)
		}
	})
	t.Run("unresolvable root fails closed", func(t *testing.T) {
		m, f, dir := newRootedManager(t)
		recordUnopened(t, m, f, "acme", "app")
		if _, err := m.Create(DatabaseConfig{Name: "b", Backend: BackendMemory}); err != nil {
			t.Fatalf("Create b: %v", err)
		}
		bRoot := *f.roots["b"]
		f.fail = map[string]error{"app": errors.New("tenant unknown")}

		_, err := m.Create(DatabaseConfig{Name: "c", Backend: BackendMemory, DataDir: filepath.Join(dir, "elsewhere")})
		var unresolved *RecordRootUnresolvedError
		if !errors.As(err, &unresolved) || unresolved.With != "app" || unresolved.TenantID != "acme" {
			t.Fatalf("Create c: err = %v, want *RecordRootUnresolvedError with acme/app", err)
		}
		if _, err := m.Get("c"); err == nil {
			t.Fatal("the refused database is registered")
		}

		err = m.Drop(context.Background(), "b")
		var dropErr *DropError
		if !errors.As(err, &dropErr) || dropErr.Name != "b" || !errors.As(err, &unresolved) {
			t.Fatalf("Drop b: err = %v, want a *DropError naming b with a *RecordRootUnresolvedError", err)
		}
		if _, err := os.Stat(bRoot); err != nil {
			t.Fatalf("b's root after the refused drop: %v", err)
		}
		if _, err := m.Get("b"); err != nil {
			t.Fatalf("b after the refused drop: %v", err)
		}
	})
}

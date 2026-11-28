package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
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

// describingFactory is an embedded backend factory that describes every
// database's binding as binding, or fails with err.
type describingFactory struct {
	EmbeddedBackendFactory
	binding *Binding
	err     error
}

func (f *describingFactory) DescribeBinding(DatabaseConfig) (*Binding, error) {
	return f.binding, f.err
}

func newDescribingManager(t *testing.T, f BackendFactory) *Manager {
	t.Helper()
	m := newCatalogManager(slog.Default(), t.TempDir(), WithBackendFactory(f))
	t.Cleanup(func() { _ = m.Close() })
	return m
}

// TestManager_InfoCarriesTheDescribedBinding: database info and both
// listings report the block the factory describes for the record.
func TestManager_InfoCarriesTheDescribedBinding(t *testing.T) {
	f := &describingFactory{binding: &Binding{Kind: BindingLocal, Encryptor: EncryptorTenant, DatabaseBarrier: true}}
	m := newDescribingManager(t, f)
	if _, err := m.Create(DatabaseConfig{Name: "a", Backend: BackendMemory}); err != nil {
		t.Fatalf("Create a: %v", err)
	}
	want := &DatabaseBinding{Kind: BindingLocal, Encryption: "tenant+database", Generation: 1}
	db, err := m.Get("a")
	if err != nil {
		t.Fatalf("Get a: %v", err)
	}
	if got := db.Info().Binding; !reflect.DeepEqual(got, want) {
		t.Fatalf("Info().Binding = %+v, want %+v", got, want)
	}
	found := false
	for _, info := range m.List() {
		if info.Name == "a" {
			found = true
			if !reflect.DeepEqual(info.Binding, want) {
				t.Fatalf("List binding = %+v, want %+v", info.Binding, want)
			}
		}
	}
	if !found {
		t.Fatal("List does not report a")
	}
	infos, err := m.ListForTenant(context.Background())
	if err != nil {
		t.Fatalf("ListForTenant: %v", err)
	}
	found = false
	for _, info := range infos {
		if info.Name == "a" {
			found = true
			if !reflect.DeepEqual(info.Binding, want) {
				t.Fatalf("ListForTenant binding = %+v, want %+v", info.Binding, want)
			}
		}
	}
	if !found {
		t.Fatal("ListForTenant does not report a")
	}
}

// TestManager_RaftBindingNamesReplicaNodes pins that a raft binding with no
// fixed nodes takes them from the installed replica reader, both when it
// is described and when Info is served, and that removing the reader
// leaves the block without nodes.
func TestManager_RaftBindingNamesReplicaNodes(t *testing.T) {
	f := &describingFactory{binding: &Binding{Kind: BindingRaft, Encryptor: EncryptorTenant, Raft: &RaftBinding{}}}
	m := newDescribingManager(t, f)
	a, err := m.Create(DatabaseConfig{Name: "a", Backend: BackendMemory})
	if err != nil {
		t.Fatalf("Create a: %v", err)
	}
	if got := a.Info().Binding; got == nil || len(got.Nodes) != 0 {
		t.Fatalf("Info().Binding without a reader = %+v, want a block without nodes", got)
	}

	want := []string{"n1", "n2"}
	var asked []string
	m.SetRaftReplicaNodes(func(tenantID, db string) []string {
		asked = append(asked, db)
		return want
	})
	if got := a.Info().Binding; got == nil || !reflect.DeepEqual(got.Nodes, want) {
		t.Fatalf("Info().Binding with a reader = %+v, want nodes %v", got, want)
	}
	if len(asked) == 0 || asked[0] != "a" {
		t.Fatalf("reader asked for %v, want a", asked)
	}

	b, err := m.Create(DatabaseConfig{Name: "b", Backend: BackendMemory})
	if err != nil {
		t.Fatalf("Create b: %v", err)
	}
	if got := b.Info().Binding; got == nil || !reflect.DeepEqual(got.Nodes, want) {
		t.Fatalf("b Info().Binding = %+v, want nodes %v", got, want)
	}

	m.SetRaftReplicaNodes(nil)
	if got := a.Info().Binding; got == nil || len(got.Nodes) != 0 {
		t.Fatalf("Info().Binding after the reader is removed = %+v, want no nodes", got)
	}
	if fn := m.bindingNodesFor(DatabaseMeta{Name: "a"}, &DatabaseBinding{Kind: BindingLocal}); fn != nil {
		t.Fatal("a local binding has a replica node reader")
	}
	if fn := m.bindingNodesFor(DatabaseMeta{Name: "a"}, nil); fn != nil {
		t.Fatal("a missing binding has a replica node reader")
	}
}

// recordBindingFactory resolves each record's binding by name: a name in
// errs fails, a name missing from bindings resolves to no binding.
type recordBindingFactory struct {
	EmbeddedBackendFactory
	bindings map[string]*Binding
	errs     map[string]error
}

func (f *recordBindingFactory) RecordBinding(cfg DatabaseConfig) (*Binding, error) {
	if err := f.errs[cfg.Name]; err != nil {
		return nil, err
	}
	return f.bindings[cfg.Name], nil
}

// TestManager_RecordBindings pins RecordBindings: no resolver means no
// bindings, a record resolving to nil is skipped, and a resolver failure
// names the record.
func TestManager_RecordBindings(t *testing.T) {
	ctx := context.Background()
	t.Run("factory without a resolver", func(t *testing.T) {
		m := newDescribingManager(t, &EmbeddedBackendFactory{})
		if _, err := m.Create(DatabaseConfig{Name: "a", Backend: BackendMemory}); err != nil {
			t.Fatalf("Create a: %v", err)
		}
		got, err := m.RecordBindings(ctx)
		if err != nil || got != nil {
			t.Fatalf("RecordBindings = %v, %v; want nil, nil", got, err)
		}
	})
	t.Run("nil bindings are skipped", func(t *testing.T) {
		want := &Binding{Kind: BindingLocal, Encryptor: EncryptorTenant}
		f := &recordBindingFactory{bindings: map[string]*Binding{"a": want}}
		m := newDescribingManager(t, f)
		for _, name := range []string{"a", "b"} {
			if _, err := m.Create(DatabaseConfig{TenantID: "acme", Name: name, Backend: BackendMemory}); err != nil {
				t.Fatalf("Create %s: %v", name, err)
			}
		}
		got, err := m.RecordBindings(ctx)
		if err != nil {
			t.Fatalf("RecordBindings: %v", err)
		}
		if len(got) != 1 || got[0] != want {
			t.Fatalf("RecordBindings = %v, want exactly a's binding", got)
		}
	})
	t.Run("resolver error names the record", func(t *testing.T) {
		cause := errors.New("tenant unknown")
		f := &recordBindingFactory{errs: map[string]error{"a": cause}}
		m := newDescribingManager(t, f)
		if _, err := m.Create(DatabaseConfig{TenantID: "acme", Name: "a", Backend: BackendMemory}); err != nil {
			t.Fatalf("Create a: %v", err)
		}
		got, err := m.RecordBindings(ctx)
		var unresolved *RecordRootUnresolvedError
		if !errors.As(err, &unresolved) || unresolved.Name != "a" || unresolved.TenantID != "acme" {
			t.Fatalf("RecordBindings = %v, %v; want *RecordRootUnresolvedError for acme/a", got, err)
		}
		if got != nil {
			t.Fatalf("RecordBindings returned %v with the error", got)
		}
		if errors.Unwrap(unresolved) != cause {
			t.Fatalf("Unwrap = %v, want %v", errors.Unwrap(unresolved), cause)
		}
	})
}

func TestManager_RecreateGivesHigherGeneration(t *testing.T) {
	ctx := context.Background()
	f := &describingFactory{binding: &Binding{Kind: BindingLocal, Encryptor: EncryptorNode}}
	m := newDescribingManager(t, f)
	first, err := m.Create(DatabaseConfig{Name: "a", Backend: BackendMemory})
	if err != nil {
		t.Fatalf("Create a: %v", err)
	}
	g1 := first.Info().Binding.Generation
	if g1 == 0 {
		t.Fatal("a created database reports generation 0")
	}
	if err := first.Backend().Put(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := m.Drop(ctx, "a"); err != nil {
		t.Fatalf("Drop a: %v", err)
	}
	second, err := m.Create(DatabaseConfig{Name: "a", Backend: BackendMemory})
	if err != nil {
		t.Fatalf("Create a again: %v", err)
	}
	if g2 := second.Info().Binding.Generation; g2 <= g1 {
		t.Fatalf("recreated generation = %d, want above %d", g2, g1)
	}
	if v, err := second.Backend().Get(ctx, []byte("k")); err != nil || v != nil {
		t.Fatalf("recreated database Get(k) = %q, %v; want empty", v, err)
	}
}

// TestManager_InfoWithoutDescriberHasNoBinding: a factory that describes
// nothing (raft mode until raft bindings) leaves the block absent.
func TestManager_InfoWithoutDescriberHasNoBinding(t *testing.T) {
	m := newDescribingManager(t, &EmbeddedBackendFactory{})
	db, err := m.Create(DatabaseConfig{Name: "a", Backend: BackendMemory})
	if err != nil {
		t.Fatalf("Create a: %v", err)
	}
	if got := db.Info().Binding; got != nil {
		t.Fatalf("Info().Binding = %+v, want nil", got)
	}
}

// TestManager_DescribeBindingErrorLeavesInfoWithoutBlock: info never fails
// over the block; an unresolvable binding leaves it out.
func TestManager_DescribeBindingErrorLeavesInfoWithoutBlock(t *testing.T) {
	m := newDescribingManager(t, &describingFactory{err: errors.New("unresolvable")})
	db, err := m.Create(DatabaseConfig{Name: "a", Backend: BackendMemory})
	if err != nil {
		t.Fatalf("Create a: %v", err)
	}
	if got := db.Info().Binding; got != nil {
		t.Fatalf("Info().Binding = %+v, want nil", got)
	}
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

// raftUnrootedFactory is a rootedFactory whose raft placeholder owns no root,
// as a raft-mode node's is; RecordRoot still resolves the group's root.
type raftUnrootedFactory struct{ rootedFactory }

func (f *raftUnrootedFactory) Create(cfg DatabaseConfig, dataDir string) (Backend, error) {
	if cfg.Backend == BackendRaft {
		return f.EmbeddedBackendFactory.Create(cfg, dataDir)
	}
	return f.rootedFactory.Create(cfg, dataDir)
}

// TestManager_RaftRootCollidesWithLocalRoot: a raft database's resolved root is
// checked against every record at create, in both orders.
func TestManager_RaftRootCollidesWithLocalRoot(t *testing.T) {
	raftCfg := DatabaseConfig{Name: "ledger", Backend: BackendRaft}
	t.Run("local then raft", func(t *testing.T) {
		dir := t.TempDir()
		f := &raftUnrootedFactory{rootedFactory{node: dir, roots: map[string]*string{}}}
		m := newCatalogManager(slog.Default(), dir, WithBackendFactory(f))
		t.Cleanup(func() { _ = m.Close() })
		m.SetShardIDClaimer(testShardClaimer(t))
		if _, err := m.Create(DatabaseConfig{Name: "local", Backend: BackendMemory, DataDir: filepath.Join(dir, "tenants", "system", "databases", "ledger")}); err != nil {
			t.Fatalf("Create local: %v", err)
		}
		_, err := m.Create(raftCfg)
		var conflict *DataDirConflictError
		if !errors.As(err, &conflict) || conflict.With != "local" {
			t.Fatalf("Create ledger: err = %v, want *DataDirConflictError with local", err)
		}
		if _, err := m.Get("ledger"); err == nil {
			t.Fatal("the refused raft database is registered")
		}
	})
	t.Run("raft then local", func(t *testing.T) {
		m, _, dir := newRootedManager(t)
		m.SetShardIDClaimer(testShardClaimer(t))
		if _, err := m.Create(raftCfg); err != nil {
			t.Fatalf("Create ledger: %v", err)
		}
		_, err := m.Create(DatabaseConfig{Name: "local", Backend: BackendMemory, DataDir: filepath.Join(dir, "tenants", "system", "databases", "ledger")})
		var conflict *DataDirConflictError
		if !errors.As(err, &conflict) || conflict.With != "ledger" {
			t.Fatalf("Create local: err = %v, want *DataDirConflictError with ledger", err)
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

// reservingFactory is a rootedFactory that reports node stores outside the
// data root, as the node binding factory does for an absolute wal_dir.
type reservingFactory struct {
	*rootedFactory
	reserved []string
	err      error
}

func (f *reservingFactory) ReservedRoots() ([]string, error) { return f.reserved, f.err }

// TestManager_DataDirOverNodeStoresRefused proves a data directory equal to,
// inside or above a node store is refused at create: the control-plane
// engine stores under the data root, and a store the factory reports outside
// it (an external write-ahead log directory).
func TestManager_DataDirOverNodeStoresRefused(t *testing.T) {
	dir := t.TempDir()
	external := filepath.Join(t.TempDir(), "ext", "wal")
	objects := filepath.Join(t.TempDir(), "objects")
	f := &reservingFactory{rootedFactory: &rootedFactory{node: dir, roots: map[string]*string{}}, reserved: []string{external, objects, objects + ".journal"}}
	m := newCatalogManager(slog.Default(), dir, WithBackendFactory(f))
	t.Cleanup(func() { _ = m.Close() })
	cases := []string{
		filepath.Join(dir, "pebble"),
		filepath.Join(dir, "pebble", "inner"),
		filepath.Join(dir, "file"),
		filepath.Join(dir, "vector"),
		filepath.Join(dir, "objstore"),
		filepath.Join(dir, "objstore.journal"),
		filepath.Join(dir, "objstore.journal", "inner"),
		objects + ".journal",
		filepath.Join(objects+".journal", "inner"),
		external,
		filepath.Join(external, "inner"),
		filepath.Dir(external),
	}
	for i, dataDir := range cases {
		_, err := m.Create(DatabaseConfig{Name: fmt.Sprintf("db%d", i), Backend: BackendFile, DataDir: dataDir})
		var reserved *DataDirReservedPathError
		if !errors.As(err, &reserved) {
			t.Fatalf("Create with data_dir %s: err = %v, want *DataDirReservedPathError", dataDir, err)
		}
	}
	if _, err := m.Create(DatabaseConfig{Name: "ok", Backend: BackendFile, DataDir: filepath.Join(t.TempDir(), "own")}); err != nil {
		t.Fatalf("Create with an unreserved data_dir: %v", err)
	}
}

// TestManager_ReservedObjstoreLSMDirsRefused proves a database data_dir on
// the objstore backend's local Pebble directory or its object cache, or
// inside either, is refused even with no factory reporting them.
func TestManager_ReservedObjstoreLSMDirsRefused(t *testing.T) {
	dir := t.TempDir()
	m := newCatalogManager(slog.Default(), dir)
	t.Cleanup(func() { _ = m.Close() })
	for i, dataDir := range []string{
		filepath.Join(dir, "objstore-lsm"),
		filepath.Join(dir, "objstore-lsm", "inner"),
		filepath.Join(dir, "objstore-lsm-cache"),
		filepath.Join(dir, "objstore-lsm-cache", "inner"),
	} {
		_, err := m.Create(DatabaseConfig{Name: fmt.Sprintf("db%d", i), Backend: BackendFile, DataDir: dataDir})
		var reserved *DataDirReservedPathError
		if !errors.As(err, &reserved) {
			t.Fatalf("Create with data_dir %s: err = %v, want *DataDirReservedPathError", dataDir, err)
		}
	}
}

// TestManager_DropNeverRemovesANodeStore proves a drop whose owned root is a
// node store (a record written before the reservation) is refused and the
// store's files stay.
func TestManager_DropNeverRemovesANodeStore(t *testing.T) {
	m, f, dir := newRootedManager(t)
	if _, err := m.Create(DatabaseConfig{Name: "b", Backend: BackendMemory}); err != nil {
		t.Fatalf("Create b: %v", err)
	}
	control := filepath.Join(dir, "pebble")
	if err := os.MkdirAll(control, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(control, "MANIFEST")
	if err := os.WriteFile(marker, []byte("control plane"), 0o600); err != nil {
		t.Fatal(err)
	}
	*f.roots["b"] = control
	err := m.Drop(context.Background(), "b")
	var reserved *DataDirReservedPathError
	if !errors.As(err, &reserved) {
		t.Fatalf("Drop b: err = %v, want *DataDirReservedPathError", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("control-plane store after the refused drop: %v", err)
	}
}

// TestManager_SymlinkedReservedSubdirIsResolved: a built-in reserved subtree
// that is a symlink (root/pebble -> /mnt/control) is compared at its target.
// A data_dir at the target, or inside it, is refused, and a drop of a record
// whose root is the target never removes it.
func TestManager_SymlinkedReservedSubdirIsResolved(t *testing.T) {
	m, f, dir := newRootedManager(t)
	control := filepath.Join(t.TempDir(), "control")
	if err := os.MkdirAll(control, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(control, "MANIFEST")
	if err := os.WriteFile(marker, []byte("control plane"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(control, filepath.Join(dir, "pebble")); err != nil {
		t.Fatal(err)
	}
	for i, dataDir := range []string{control, filepath.Join(control, "inner")} {
		_, err := m.Create(DatabaseConfig{Name: fmt.Sprintf("db%d", i), Backend: BackendFile, DataDir: dataDir})
		var reserved *DataDirReservedPathError
		if !errors.As(err, &reserved) {
			t.Fatalf("Create with data_dir %s: err = %v, want *DataDirReservedPathError", dataDir, err)
		}
	}

	if _, err := m.Create(DatabaseConfig{Name: "b", Backend: BackendMemory}); err != nil {
		t.Fatalf("Create b: %v", err)
	}
	*f.roots["b"] = control
	err := m.Drop(context.Background(), "b")
	var reserved *DataDirReservedPathError
	if !errors.As(err, &reserved) {
		t.Fatalf("Drop b: err = %v, want *DataDirReservedPathError", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("control-plane store after the refused drop: %v", err)
	}
}

// TestManager_DropNeverRemovesTheObjstoreJournal proves a record whose root is
// the node object store's commit-journal sibling, which the factory reports,
// is refused at drop and the journal stays.
func TestManager_DropNeverRemovesTheObjstoreJournal(t *testing.T) {
	dir := t.TempDir()
	objects := filepath.Join(t.TempDir(), "objects")
	journal := objects + ".journal"
	f := &reservingFactory{rootedFactory: &rootedFactory{node: dir, roots: map[string]*string{}}, reserved: []string{objects, journal}}
	m := newCatalogManager(slog.Default(), dir, WithBackendFactory(f))
	t.Cleanup(func() { _ = m.Close() })
	if _, err := m.Create(DatabaseConfig{Name: "b", Backend: BackendMemory}); err != nil {
		t.Fatalf("Create b: %v", err)
	}
	if err := os.MkdirAll(journal, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(journal, "commit")
	if err := os.WriteFile(marker, []byte("journal"), 0o600); err != nil {
		t.Fatal(err)
	}
	*f.roots["b"] = journal
	err := m.Drop(context.Background(), "b")
	var reserved *DataDirReservedPathError
	if !errors.As(err, &reserved) {
		t.Fatalf("Drop b: err = %v, want *DataDirReservedPathError", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("object-store journal after the refused drop: %v", err)
	}
}

// TestManager_ReservedRootsErrorFailsCreateClosed proves a factory that cannot
// resolve its node stores refuses a data directory and writes no record.
func TestManager_ReservedRootsErrorFailsCreateClosed(t *testing.T) {
	dir := t.TempDir()
	f := &reservingFactory{rootedFactory: &rootedFactory{node: dir, roots: map[string]*string{}}, err: errors.New("unresolvable store")}
	m := newCatalogManager(slog.Default(), dir, WithBackendFactory(f))
	t.Cleanup(func() { _ = m.Close() })
	_, err := m.Create(DatabaseConfig{Name: "db", Backend: BackendFile, DataDir: filepath.Join(t.TempDir(), "own")})
	var invalid *InvalidDataDirError
	if !errors.As(err, &invalid) {
		t.Fatalf("Create: err = %v, want *InvalidDataDirError", err)
	}
	if _, err := m.Get("db"); err == nil {
		t.Fatal("a refused database is registered")
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

// isolatedHomes routes the tenants in homes to their own home store, as a
// tenant registry does for an isolated tenant, and lists every tenant it
// holds (CatalogTenantLister).
type isolatedHomes struct {
	homes map[string]kvstore.KVStore
}

func (h *isolatedHomes) Resolve(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	if s, ok := h.homes[tenantID]; ok {
		return s, nil
	}
	return nil, errors.New("no home for " + tenantID)
}

func (h *isolatedHomes) InHome(_ context.Context, tenantID string) (bool, error) {
	_, ok := h.homes[tenantID]
	return ok, nil
}

func (h *isolatedHomes) TenantIDs(context.Context) ([]string, error) {
	ids := make([]string, 0, len(h.homes))
	for id := range h.homes {
		ids = append(ids, id)
	}
	return ids, nil
}

// An isolated tenant's records are in its home store, which Catalog.List
// does not read: the root check reads every tenant's records, and refuses
// any root under another tenant's tenants/<id>/ subtree, so neither a
// create nor a drop of a system database reaches an unopened isolated
// tenant's root.
func TestManager_RootsCheckIsolatedTenantRecords(t *testing.T) {
	newIsolated := func(t *testing.T) (*Manager, *rootedFactory, string, string) {
		t.Helper()
		m, f, dir := newRootedManager(t)
		cat, err := NewKVCatalog(kvstore.NewMemoryKVStore(), &isolatedHomes{homes: map[string]kvstore.KVStore{"iso": kvstore.NewMemoryKVStore()}})
		if err != nil {
			t.Fatalf("NewKVCatalog: %v", err)
		}
		m.SetCatalog(cat)
		isoRoot := filepath.Join(dir, "iso-root", "app")
		if err := os.MkdirAll(isoRoot, 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := cat.Create(context.Background(), DatabaseMeta{TenantID: "iso", Name: "app", Backend: BackendMemory, DataDir: isoRoot}); err != nil {
			t.Fatalf("record iso/app: %v", err)
		}
		if metas, err := cat.List(context.Background()); err != nil || len(metas) != 0 {
			t.Fatalf("List = %v, %v; the isolated record must be in the home store only", metas, err)
		}
		return m, f, dir, isoRoot
	}

	t.Run("create over an isolated tenant's root", func(t *testing.T) {
		m, _, dir, isoRoot := newIsolated(t)
		_, err := m.Create(DatabaseConfig{Name: "evil", Backend: BackendMemory, DataDir: filepath.Join(dir, "iso-root")})
		var conflict *DataDirConflictError
		if !errors.As(err, &conflict) || conflict.With != "app" {
			t.Fatalf("Create evil: err = %v, want *DataDirConflictError with app", err)
		}
		if _, err := m.Get("evil"); err == nil {
			t.Fatal("the refused database is registered")
		}
		if _, err := os.Stat(isoRoot); err != nil {
			t.Fatalf("iso/app's root after the refused create: %v", err)
		}
	})
	t.Run("drop over an isolated tenant's root", func(t *testing.T) {
		m, f, dir, isoRoot := newIsolated(t)
		if _, err := m.Create(DatabaseConfig{Name: "b", Backend: BackendMemory}); err != nil {
			t.Fatalf("Create b: %v", err)
		}
		*f.roots["b"] = filepath.Join(dir, "iso-root")
		err := m.Drop(context.Background(), "b")
		var conflict *DataDirConflictError
		if !errors.As(err, &conflict) || conflict.With != "app" {
			t.Fatalf("Drop b: err = %v, want a *DataDirConflictError with app", err)
		}
		if _, err := os.Stat(isoRoot); err != nil {
			t.Fatalf("iso/app's root after the refused drop: %v", err)
		}
	})
	t.Run("create under another tenant's subtree", func(t *testing.T) {
		m, _, dir, _ := newIsolated(t)
		other := filepath.Join(dir, "tenants", "other", "databases", "x")
		_, err := m.Create(DatabaseConfig{Name: "evil", Backend: BackendMemory, DataDir: other})
		var conflict *DataDirConflictError
		if !errors.As(err, &conflict) || conflict.With != "tenant other" {
			t.Fatalf("Create evil: err = %v, want *DataDirConflictError with tenant other", err)
		}
		if _, err := m.Get("evil"); err == nil {
			t.Fatal("the refused database is registered")
		}
	})
}

// sealableHomes is isolatedHomes whose listed tenants can be sealed: a sealed
// tenant's home resolves to tenant.ErrTenantSealed, the way the tenant
// package's resolver answers for a barrier sealed on this node.
type sealableHomes struct {
	isolatedHomes
	sealed map[string]bool
}

func (h *sealableHomes) Resolve(ctx context.Context, tenantID string) (kvstore.KVStore, error) {
	if h.sealed[tenantID] {
		return nil, tenant.ErrTenantSealed
	}
	return h.isolatedHomes.Resolve(ctx, tenantID)
}

func (h *sealableHomes) TenantIDs(context.Context) ([]string, error) {
	ids := make([]string, 0, len(h.homes))
	for id := range h.homes {
		ids = append(ids, id)
	}
	return ids, nil
}

// TestManager_SealedTenantNeitherBlocksNorEscapesRoots proves one sealed
// isolated tenant does not block another tenant's create and drop, nor hide
// healthy databases from maintenance, while its own database's root stays
// reserved through the root claim recorded at create.
func TestManager_SealedTenantNeitherBlocksNorEscapesRoots(t *testing.T) {
	ctx := context.Background()
	newSealed := func(t *testing.T) (*Manager, *KVCatalog, *sealableHomes, string, string) {
		t.Helper()
		m, _, dir := newRootedManager(t)
		homes := &sealableHomes{
			isolatedHomes: isolatedHomes{homes: map[string]kvstore.KVStore{"iso": kvstore.NewMemoryKVStore()}},
			sealed:        map[string]bool{},
		}
		cat, err := NewKVCatalog(kvstore.NewMemoryKVStore(), homes)
		if err != nil {
			t.Fatalf("NewKVCatalog: %v", err)
		}
		m.SetCatalog(cat)
		isoRoot := filepath.Join(dir, "iso-root", "app")
		return m, cat, homes, dir, isoRoot
	}

	t.Run("claimed at create", func(t *testing.T) {
		m, cat, homes, dir, isoRoot := newSealed(t)
		if err := cat.Create(ctx, DatabaseMeta{TenantID: "iso", Name: "app", Incarnation: "i1", Backend: BackendMemory, DataDir: isoRoot}); err != nil {
			t.Fatalf("record iso/app: %v", err)
		}
		homes.sealed["iso"] = true

		if _, err := m.Create(DatabaseConfig{Name: "a", Backend: BackendMemory, DataDir: filepath.Join(dir, "elsewhere")}); err != nil {
			t.Fatalf("Create a with iso sealed: %v", err)
		}
		if err := m.Drop(ctx, "a"); err != nil {
			t.Fatalf("Drop a with iso sealed: %v", err)
		}
		_, err := m.Create(DatabaseConfig{Name: "evil", Backend: BackendMemory, DataDir: filepath.Join(dir, "iso-root")})
		var conflict *DataDirConflictError
		if !errors.As(err, &conflict) || conflict.With != "app" {
			t.Fatalf("Create evil over the sealed tenant's root: err = %v, want *DataDirConflictError with app", err)
		}
	})

	t.Run("maintenance lists healthy databases", func(t *testing.T) {
		m, cat, homes, _, isoRoot := newSealed(t)
		if err := cat.Create(ctx, DatabaseMeta{TenantID: "iso", Name: "app", Incarnation: "i1", Backend: BackendMemory, DataDir: isoRoot}); err != nil {
			t.Fatalf("record iso/app: %v", err)
		}
		if _, err := m.Create(DatabaseConfig{Name: "healthy", Backend: BackendMemory}); err != nil {
			t.Fatalf("Create healthy: %v", err)
		}
		homes.sealed["iso"] = true

		metas, err := cat.ListAll(ctx)
		var skipped *TenantsSkippedError
		if !errors.As(err, &skipped) || len(skipped.Tenants) != 1 || skipped.Tenants[0].TenantID != "iso" {
			t.Fatalf("ListAll err = %v, want *TenantsSkippedError naming iso", err)
		}
		if !errors.Is(err, tenant.ErrTenantSealed) {
			t.Fatalf("ListAll err = %v, want it to match tenant.ErrTenantSealed", err)
		}
		if len(metas) != 1 || metas[0].Name != "healthy" {
			t.Fatalf("ListAll metas = %v, want the healthy database", metas)
		}
		records, err := m.ListRecords(ctx)
		if err != nil || len(records) != 1 || records[0].Name != "healthy" {
			t.Fatalf("ListRecords = %v, %v; want the healthy database", records, err)
		}
	})

	t.Run("unindexed sealed tenant fails closed until listed unsealed", func(t *testing.T) {
		m, cat, homes, dir, isoRoot := newSealed(t)
		// A record written before root claims existed: in the home only.
		data, err := json.Marshal(DatabaseMeta{TenantID: "iso", Name: "app", Incarnation: "i1", Backend: BackendMemory, DataDir: isoRoot})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if err := homes.homes["iso"].Put(ctx, CatalogKey("iso", "app"), data); err != nil {
			t.Fatalf("legacy record: %v", err)
		}
		homes.sealed["iso"] = true
		_, err = m.Create(DatabaseConfig{Name: "a", Backend: BackendMemory, DataDir: filepath.Join(dir, "elsewhere")})
		var unresolved *RecordRootUnresolvedError
		if !errors.As(err, &unresolved) {
			t.Fatalf("Create a over an unindexed sealed tenant: err = %v, want *RecordRootUnresolvedError", err)
		}

		homes.sealed["iso"] = false
		if _, err := cat.ListAll(ctx); err != nil {
			t.Fatalf("ListAll unsealed: %v", err)
		}
		homes.sealed["iso"] = true
		if _, err := m.Create(DatabaseConfig{Name: "a", Backend: BackendMemory, DataDir: filepath.Join(dir, "elsewhere")}); err != nil {
			t.Fatalf("Create a once iso is indexed: %v", err)
		}
		_, err = m.Create(DatabaseConfig{Name: "evil", Backend: BackendMemory, DataDir: filepath.Join(dir, "iso-root")})
		var conflict *DataDirConflictError
		if !errors.As(err, &conflict) || conflict.With != "app" {
			t.Fatalf("Create evil over the indexed sealed root: err = %v, want *DataDirConflictError with app", err)
		}
	})

	t.Run("delete releases the claim", func(t *testing.T) {
		m, cat, homes, dir, isoRoot := newSealed(t)
		if err := cat.Create(ctx, DatabaseMeta{TenantID: "iso", Name: "app", Incarnation: "i1", Backend: BackendMemory, DataDir: isoRoot}); err != nil {
			t.Fatalf("record iso/app: %v", err)
		}
		if err := cat.Delete(ctx, "iso", "app", "i1", ""); err != nil {
			t.Fatalf("Delete iso/app: %v", err)
		}
		homes.sealed["iso"] = true
		if _, err := m.Create(DatabaseConfig{Name: "reuse", Backend: BackendMemory, DataDir: filepath.Join(dir, "iso-root")}); err != nil {
			t.Fatalf("Create over a deleted sealed tenant's root: %v", err)
		}
	})
}

// TestManager_RootFreedAfterAFailedMaterializedCreate proves an isolated
// tenant's create whose record write fails after its root claim leaves no
// claim behind: once the tenant seals, a create of another database on the
// same root is not refused by the failed create's claim.
func TestManager_RootFreedAfterAFailedMaterializedCreate(t *testing.T) {
	ctx := context.Background()
	m, _, dir := newRootedManager(t)
	boom := errors.New("home write failed")
	home := &recordFailingStore{MemoryKVStore: kvstore.NewMemoryKVStore(), fail: boom}
	homes := &sealableHomes{
		isolatedHomes: isolatedHomes{homes: map[string]kvstore.KVStore{"iso": home}},
		sealed:        map[string]bool{},
	}
	system := kvstore.NewMemoryKVStore()
	cat, err := NewKVCatalog(system, homes)
	if err != nil {
		t.Fatalf("NewKVCatalog: %v", err)
	}
	m.SetCatalog(cat)
	isoRoot := filepath.Join(dir, "iso-root", "app")

	if _, err := m.Create(DatabaseConfig{TenantID: "iso", Name: "app", Backend: BackendMemory, DataDir: isoRoot}); !errors.Is(err, boom) {
		t.Fatalf("Create iso/app over a failing home: err = %v, want the home write error", err)
	}
	if _, err := m.Get("app"); err == nil {
		t.Fatal("the failed database is registered")
	}
	var leftover []string
	if err := system.Scan(ctx, RootClaimKeyPrefix, func(key string, _ []byte) error {
		leftover = append(leftover, key)
		return nil
	}); err != nil {
		t.Fatalf("Scan claims: %v", err)
	}
	if len(leftover) != 0 {
		t.Fatalf("root claims after the failed create = %v, want none", leftover)
	}

	home.fail = nil
	homes.sealed["iso"] = true
	if _, err := m.Create(DatabaseConfig{Name: "reuse", Backend: BackendMemory, DataDir: filepath.Join(dir, "iso-root")}); err != nil {
		t.Fatalf("Create reuse over the failed create's root: %v", err)
	}
}

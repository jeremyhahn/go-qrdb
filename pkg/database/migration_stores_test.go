package database

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// migrationFactory resolves record bindings by database name and, for a
// raft record without one, describes the binding by name too.
type migrationFactory struct {
	EmbeddedBackendFactory
	bindings  map[string]*Binding
	described map[string]*Binding
	errs      map[string]error
	descErr   error
}

func (f *migrationFactory) RecordBinding(cfg DatabaseConfig) (*Binding, error) {
	if err := f.errs[cfg.Name]; err != nil {
		return nil, err
	}
	return f.bindings[cfg.Name], nil
}

func (f *migrationFactory) DescribeBinding(cfg DatabaseConfig) (*Binding, error) {
	if f.descErr != nil {
		return nil, f.descErr
	}
	return f.described[cfg.Name], nil
}

// newMigrationManager writes names as memory records of the system
// tenant and raftNames as raft records straight to the catalog.
func newMigrationManager(t *testing.T, f BackendFactory, names, raftNames []string) *Manager {
	t.Helper()
	m := newDescribingManager(t, f)
	cat, err := NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	if err != nil {
		t.Fatalf("NewKVCatalog: %v", err)
	}
	m.SetCatalog(cat)
	for _, name := range names {
		if err := cat.Create(context.Background(), DatabaseMeta{Name: name, Incarnation: "i-" + name, Backend: BackendMemory}); err != nil {
			t.Fatalf("record %s: %v", name, err)
		}
	}
	for _, name := range raftNames {
		if err := cat.Create(context.Background(), DatabaseMeta{Name: name, Incarnation: "i-" + name, Backend: BackendRaft}); err != nil {
			t.Fatalf("record %s: %v", name, err)
		}
	}
	return m
}

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNodeEncryptedStoreDirs_NilManager(t *testing.T) {
	p, f, err := NodeEncryptedStoreDirs(context.Background(), nil, "")
	if p != nil || f != nil || err != nil {
		t.Fatalf("NodeEncryptedStoreDirs(nil) = %v, %v, %v; want nothing", p, f, err)
	}
}

// Only node-encrypted stores with a data root that exist on disk are
// listed: a tenant-encrypted store, a memory store and a missing store
// directory are not.
func TestNodeEncryptedStoreDirs_ListsExistingNodeStores(t *testing.T) {
	root := t.TempDir()
	peb, file, tenantPeb, missing := filepath.Join(root, "p"), filepath.Join(root, "f"), filepath.Join(root, "t"), filepath.Join(root, "m")
	mkdirs(t, filepath.Join(peb, "pebble"), filepath.Join(file, "file"), filepath.Join(tenantPeb, "pebble"))
	f := &migrationFactory{bindings: map[string]*Binding{
		"peb":     {Backend: BackendPebble, DataRoot: peb, Encryptor: EncryptorNode},
		"file":    {Backend: BackendFile, DataRoot: file, Encryptor: EncryptorNode},
		"tenant":  {Backend: BackendPebble, DataRoot: tenantPeb, Encryptor: EncryptorTenant},
		"mem":     {Backend: BackendMemory, Encryptor: EncryptorNode},
		"missing": {Backend: BackendPebble, DataRoot: missing, Encryptor: EncryptorNode},
	}}
	m := newMigrationManager(t, f, []string{"peb", "file", "tenant", "mem", "missing"}, nil)
	pebbleDirs, fileDirs, err := NodeEncryptedStoreDirs(context.Background(), m, "")
	if err != nil {
		t.Fatalf("NodeEncryptedStoreDirs: %v", err)
	}
	if want := []string{filepath.Join(peb, "pebble")}; !reflect.DeepEqual(pebbleDirs, want) {
		t.Fatalf("pebble dirs = %v, want %v", pebbleDirs, want)
	}
	if want := []string{filepath.Join(file, "file")}; !reflect.DeepEqual(fileDirs, want) {
		t.Fatalf("file dirs = %v, want %v", fileDirs, want)
	}

	// A missing pebble store is no store to move, so an external WAL
	// directory does not refuse it.
	f.bindings = map[string]*Binding{"missing": f.bindings["missing"]}
	if p, _, err := NodeEncryptedStoreDirs(context.Background(), m, filepath.Join(root, "wal")); err != nil || p != nil {
		t.Fatalf("missing store with a wal_dir = %v, %v; want nothing", p, err)
	}
}

// A store the migration cannot move is refused, naming the database.
func TestNodeEncryptedStoreDirs_RefusesUnmovableStores(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, filepath.Join(root, "pebble"))
	cases := []struct {
		name    string
		binding *Binding
		walDir  string
		reason  string
	}{
		{"objstore", &Binding{Backend: BackendObjstore, DataRoot: root, Encryptor: EncryptorNode}, "", "objstore"},
		{"external wal", &Binding{Backend: BackendPebble, DataRoot: root, Encryptor: EncryptorNode}, filepath.Join(root, "wal"), "storage.wal_dir"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.binding.TenantID, tc.binding.Database = "acme", "app"
			m := newMigrationManager(t, &migrationFactory{bindings: map[string]*Binding{"app": tc.binding}}, []string{"app"}, nil)
			p, fd, err := NodeEncryptedStoreDirs(context.Background(), m, tc.walDir)
			var unsupported *StoreMigrationUnsupportedError
			if !errors.As(err, &unsupported) || p != nil || fd != nil {
				t.Fatalf("NodeEncryptedStoreDirs = %v, %v, %v; want *StoreMigrationUnsupportedError", p, fd, err)
			}
			msg := err.Error()
			if !strings.HasPrefix(msg, "database acme/app ") || !strings.Contains(msg, tc.reason) {
				t.Fatalf("Error() = %q, want it to name acme/app and %q", msg, tc.reason)
			}
		})
	}
}

// A raft record the resolver gives no binding is described: its group
// stores under the data root are listed unless the group's state runs in
// memory, and a record the resolver already bound is not described again.
func TestNodeEncryptedStoreDirs_DescribesRaftRecords(t *testing.T) {
	root := t.TempDir()
	ledger, cache := filepath.Join(root, "ledger"), filepath.Join(root, "cache")
	mkdirs(t, filepath.Join(ledger, "shard-1"), filepath.Join(cache, "shard-2"))
	if err := os.WriteFile(filepath.Join(ledger, "shard-9"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	bound := filepath.Join(root, "bound")
	mkdirs(t, filepath.Join(bound, "shard-3"))
	f := &migrationFactory{
		bindings: map[string]*Binding{
			"bound": {Backend: BackendRaft, DataRoot: bound, Encryptor: EncryptorNode, Raft: &RaftBinding{StateStorage: BackendPebble}},
		},
		described: map[string]*Binding{
			"ledger": {Backend: BackendRaft, DataRoot: ledger, Encryptor: EncryptorNode, Raft: &RaftBinding{StateStorage: BackendPebble}},
			"cache":  {Backend: BackendRaft, DataRoot: cache, Encryptor: EncryptorNode, Raft: &RaftBinding{StateStorage: BackendMemory}},
			"bound":  {Backend: BackendRaft, DataRoot: filepath.Join(root, "never"), Encryptor: EncryptorNode},
		},
	}
	m := newMigrationManager(t, f, nil, []string{"ledger", "cache", "bound", "gone"})
	pebbleDirs, fileDirs, err := NodeEncryptedStoreDirs(context.Background(), m, "")
	if err != nil {
		t.Fatalf("NodeEncryptedStoreDirs: %v", err)
	}
	got := map[string]bool{}
	for _, d := range pebbleDirs {
		got[d] = true
	}
	want := map[string]bool{filepath.Join(ledger, "shard-1"): true, filepath.Join(bound, "shard-3"): true}
	if !reflect.DeepEqual(got, want) || len(pebbleDirs) != 2 || fileDirs != nil {
		t.Fatalf("dirs = %v, %v; want pebble %v and no file dirs", pebbleDirs, fileDirs, want)
	}
}

// A failure to resolve or describe a record fails closed, naming it.
func TestNodeEncryptedStoreDirs_ResolveFailures(t *testing.T) {
	cause := errors.New("tenant unknown")
	t.Run("record binding", func(t *testing.T) {
		m := newMigrationManager(t, &migrationFactory{errs: map[string]error{"app": cause}}, []string{"app"}, nil)
		_, _, err := NodeEncryptedStoreDirs(context.Background(), m, "")
		var unresolved *RecordRootUnresolvedError
		if !errors.As(err, &unresolved) || unresolved.Name != "app" || !errors.Is(err, cause) {
			t.Fatalf("err = %v, want *RecordRootUnresolvedError for app", err)
		}
	})
	t.Run("described binding", func(t *testing.T) {
		m := newMigrationManager(t, &migrationFactory{descErr: cause}, nil, []string{"ledger"})
		_, _, err := NodeEncryptedStoreDirs(context.Background(), m, "")
		var unresolved *RecordRootUnresolvedError
		if !errors.As(err, &unresolved) || unresolved.Name != "ledger" || !errors.Is(err, cause) {
			t.Fatalf("err = %v, want *RecordRootUnresolvedError for ledger", err)
		}
	})
	t.Run("no describer", func(t *testing.T) {
		f := &recordBindingFactory{bindings: map[string]*Binding{}}
		m := newMigrationManager(t, f, nil, []string{"ledger"})
		p, fd, err := NodeEncryptedStoreDirs(context.Background(), m, "")
		if p != nil || fd != nil || err != nil {
			t.Fatalf("NodeEncryptedStoreDirs = %v, %v, %v; want nothing", p, fd, err)
		}
	})
}

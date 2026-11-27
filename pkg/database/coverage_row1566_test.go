package database

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// releaserBackend is a Backend whose engine gives its claim back: it records
// each release call and returns the error the test set.
type releaserBackend struct {
	Backend
	ownerErr error
	claimErr error
	fresh    bool
	calls    []string
}

func (r *releaserBackend) ReleaseOwner() error {
	r.calls = append(r.calls, "owner")
	return r.ownerErr
}

func (r *releaserBackend) ReleaseClaim() error {
	r.calls = append(r.calls, "claim")
	return r.claimErr
}

func (r *releaserBackend) FreshlyClaimed() bool { return r.fresh }

// plainBackend hides every optional interface of the wrapped backend.
type plainBackend struct{ Backend }

func TestBarrierBackend_ReleaseForwardsToTheWrappedEngine(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name     string
		ownerErr error
		claimErr error
		fresh    bool
	}{
		{name: "released", fresh: true},
		{name: "release fails", ownerErr: boom, claimErr: boom},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inner := &releaserBackend{Backend: NewRemoteBackend(nil), ownerErr: tc.ownerErr, claimErr: tc.claimErr, fresh: tc.fresh}
			b := NewBarrierBackend(inner, &xorBarrier{mask: 0x5a})
			if err := b.ReleaseOwner(); !errors.Is(err, tc.ownerErr) {
				t.Fatalf("ReleaseOwner = %v, want %v", err, tc.ownerErr)
			}
			if err := b.ReleaseClaim(); !errors.Is(err, tc.claimErr) {
				t.Fatalf("ReleaseClaim = %v, want %v", err, tc.claimErr)
			}
			if got := b.FreshlyClaimed(); got != tc.fresh {
				t.Fatalf("FreshlyClaimed = %v, want %v", got, tc.fresh)
			}
			if !slices.Equal(inner.calls, []string{"owner", "claim"}) {
				t.Fatalf("calls = %v, want owner then claim", inner.calls)
			}
		})
	}
}

func TestBarrierBackend_ReleaseOverAnEngineWithNothingToRelease(t *testing.T) {
	b := NewBarrierBackend(plainBackend{NewRemoteBackend(nil)}, &xorBarrier{mask: 0x5a})
	if err := b.ReleaseOwner(); err != nil {
		t.Fatalf("ReleaseOwner: %v", err)
	}
	if err := b.ReleaseClaim(); err != nil {
		t.Fatalf("ReleaseClaim: %v", err)
	}
	if b.FreshlyClaimed() {
		t.Fatal("FreshlyClaimed = true over an engine that claims nothing")
	}
	if err := b.Reclaim(); err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	if b.IsSealed() {
		t.Fatal("IsSealed = true under an unsealed barrier")
	}
}

func TestManager_RelocateTenantDataDirs(t *testing.T) {
	ctx := tenantContext("tenant-a")

	t.Run("no move", func(t *testing.T) {
		m := NewManager(nil, t.TempDir())
		defer func() { _ = m.Close() }()
		for _, roots := range [][2]string{{"", "/b"}, {"/a", ""}, {"/a", "/a/"}} {
			if err := m.RelocateTenantDataDirs(ctx, "tenant-a", roots[0], roots[1]); err != nil {
				t.Fatalf("RelocateTenantDataDirs(%q, %q) = %v, want nil", roots[0], roots[1], err)
			}
		}
	})

	t.Run("no catalog", func(t *testing.T) {
		m := NewManager(nil, t.TempDir())
		defer func() { _ = m.Close() }()
		var required *CatalogRequiredError
		if err := m.RelocateTenantDataDirs(ctx, "tenant-a", "/a", "/b"); !errors.As(err, &required) {
			t.Fatalf("RelocateTenantDataDirs = %v, want CatalogRequiredError", err)
		}
	})

	t.Run("moves the records under the old root and closes their open databases", func(t *testing.T) {
		root := t.TempDir()
		cat := newHookCatalog(t)
		m := NewManager(nil, root)
		m.SetCatalog(cat)
		defer func() { _ = m.Close() }()

		db, err := m.CreateForTenant(ctx, DatabaseConfig{Name: "orders", Backend: BackendFile})
		if err != nil {
			t.Fatalf("CreateForTenant: %v", err)
		}
		if _, err := m.CreateForTenant(ctx, validMemoryConfig("cache")); err != nil {
			t.Fatalf("CreateForTenant memory: %v", err)
		}
		oldDir := db.DataDir()
		if oldDir == "" {
			t.Fatal("file database has no data directory")
		}
		// Record the data directory the database opened at, as a create with
		// an explicit data_dir does.
		seeded, err := cat.ListTenant(ctx, "tenant-a")
		if err != nil {
			t.Fatalf("ListTenant: %v", err)
		}
		for _, meta := range seeded {
			if meta.Name != "orders" {
				continue
			}
			if err := cat.Update(ctx, meta.TenantID, meta.Name, meta.Incarnation, func(rec *DatabaseMeta) error {
				rec.DataDir = oldDir
				return nil
			}); err != nil {
				t.Fatalf("seed the data dir: %v", err)
			}
		}
		toRoot := t.TempDir()
		if err := m.RelocateTenantDataDirs(ctx, "tenant-a", root, toRoot); err != nil {
			t.Fatalf("RelocateTenantDataDirs: %v", err)
		}
		metas, err := cat.ListTenant(ctx, "tenant-a")
		if err != nil {
			t.Fatalf("ListTenant: %v", err)
		}
		var moved bool
		for _, meta := range metas {
			if meta.Name != "orders" {
				continue
			}
			rel, _ := filepath.Rel(root, oldDir)
			if want := filepath.Join(toRoot, rel); meta.DataDir != want {
				t.Fatalf("orders data dir = %q, want %q", meta.DataDir, want)
			}
			moved = true
		}
		if !moved {
			t.Fatal("orders record missing after the relocate")
		}
		if _, open := m.databases.Load(keyFor("tenant-a", "orders")); open {
			t.Fatal("orders is still open at its old root")
		}
		// A second move from the old root finds nothing under it.
		if err := m.RelocateTenantDataDirs(ctx, "tenant-a", root, t.TempDir()); err != nil {
			t.Fatalf("second RelocateTenantDataDirs: %v", err)
		}
	})
}

func TestRelocatedPath_RootsAndOutsiders(t *testing.T) {
	tests := []struct {
		dir, from, to, want string
		ok                  bool
	}{
		{dir: "", from: "/a", to: "/b"},
		{dir: "/c/x", from: "/a", to: "/b"},
		{dir: "/a", from: "/a/x", to: "/b"},
		{dir: "/a/x/y", from: "/a", to: "/b", want: "/b/x/y", ok: true},
	}
	for _, tc := range tests {
		got, ok := relocatedPath(tc.dir, tc.from, tc.to)
		if got != tc.want || ok != tc.ok {
			t.Errorf("relocatedPath(%q, %q, %q) = %q, %v; want %q, %v", tc.dir, tc.from, tc.to, got, ok, tc.want, tc.ok)
		}
	}
}

func TestManager_ResolveDefaultsCopiesTheRaftConfig(t *testing.T) {
	raft := &DatabaseRaftConfig{}
	cfg := DatabaseConfig{Name: "orders", Raft: raft}
	var nilManager *Manager
	if got := nilManager.ResolveDefaults(cfg); got.Raft == raft || got.Name != "orders" {
		t.Fatalf("nil manager ResolveDefaults = %+v, want a copied Raft config", got)
	}
	m := NewManager(nil, t.TempDir())
	defer func() { _ = m.Close() }()
	if got := m.ResolveDefaults(cfg); got.Raft == raft {
		t.Fatal("ResolveDefaults shares the caller's Raft config")
	}
}

func TestRow1566Errors(t *testing.T) {
	boom := errors.New("boom")
	if err := (&ObjstoreCredentialSettingError{Setting: "secret", Err: boom}); !errors.Is(err, boom) {
		t.Fatal("ObjstoreCredentialSettingError does not unwrap to its cause")
	}
	if msg := (&ObjectStorageNotWiredError{Name: "orders"}).Error(); !strings.Contains(msg, `"orders"`) || !strings.Contains(msg, "not wired") {
		t.Fatalf("ObjectStorageNotWiredError = %q", msg)
	}
}

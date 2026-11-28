package sqlitevfs

import (
	"testing"
)

// publishBackends returns every Backend of this package, each over a fresh
// store: the local MVCCBackend and the ServiceBackend over the service
// double.
func publishBackends(t *testing.T) map[string]Backend {
	t.Helper()
	sb, _ := newServiceBackend(t)
	return map[string]Backend{
		"mvcc":    newTestBackend(t),
		"service": sb,
	}
}

// TestBackendPublishExplicitWritesWinOverMoved runs the publish precedence
// against every backend: the moved values first, then the explicit puts,
// then the explicit deletes, so an explicit put on a moved key stores the
// explicit value and an explicit delete removes a moved key.
func TestBackendPublishExplicitWritesWinOverMoved(t *testing.T) {
	for name, b := range publishBackends(t) {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			seed := map[string][]byte{"src/1": []byte("old"), "src/2": []byte("two")}
			if _, err := b.Commit(ctx, nil, seed, nil); err != nil {
				t.Fatalf("Commit: %v", err)
			}
			rev, err := b.Publish(ctx, []byte("src/"), []byte("dst/"), 2, nil,
				map[string][]byte{"dst/1": []byte("new")}, [][]byte{[]byte("dst/2")})
			if err != nil {
				t.Fatalf("Publish: %v", err)
			}
			if v, found, err := b.GetAt(ctx, []byte("dst/1"), rev); err != nil || !found || string(v) != "new" {
				t.Fatalf("dst/1 = %q found=%v err=%v, want the explicit put \"new\"", v, found, err)
			}
			for _, key := range []string{"dst/2", "src/1", "src/2"} {
				if _, found, err := b.GetAt(ctx, []byte(key), rev); err != nil || found {
					t.Fatalf("%s after the publish: found=%v err=%v, want deleted", key, found, err)
				}
			}
		})
	}
}

// TestBackendRevisionZeroIsTheEmptyStore checks every backend reads
// revision zero as the store before any write, never as the current one.
func TestBackendRevisionZeroIsTheEmptyStore(t *testing.T) {
	for name, b := range publishBackends(t) {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			if _, err := b.Commit(ctx, nil, map[string][]byte{"k/1": []byte("v")}, nil); err != nil {
				t.Fatalf("Commit: %v", err)
			}
			if v, found, err := b.GetAt(ctx, []byte("k/1"), 0); err != nil || found {
				t.Fatalf("GetAt at revision 0 = %q found=%v err=%v, want not found", v, found, err)
			}
			seed(t, newTestStore(t, b, "app.db"), 0x01)
			txn, err := newTestStore(t, b, "app.db").BeginAt(ctx, 0)
			if err != nil {
				t.Fatalf("BeginAt(0): %v", err)
			}
			if size := txn.Size(); size != 0 {
				t.Fatalf("BeginAt(0) sees a %d-byte database, want an empty one", size)
			}
			if err := txn.Rollback(ctx); err != nil {
				t.Fatalf("Rollback: %v", err)
			}
		})
	}
}

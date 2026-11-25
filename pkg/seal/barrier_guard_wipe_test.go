package seal

import (
	"bytes"
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// TestStorageBarrier_GuardedWritesWipeTheDecryptedGuard: every guarded
// write decrypts the stored value only to compare it, and wipes that copy
// whether the guard holds or not.
func TestStorageBarrier_GuardedWritesWipeTheDecryptedGuard(t *testing.T) {
	ctx := context.Background()
	ops := map[string]func(b *StorageBarrier, expected []byte) (bool, error){
		"batch": func(b *StorageBarrier, expected []byte) (bool, error) {
			return b.PutAllIfAllUnchanged(ctx, []kvstore.Guard{{Key: "held", Value: expected}},
				[]kvstore.KV{{Key: "held", Value: []byte("new")}})
		},
		"swap": func(b *StorageBarrier, expected []byte) (bool, error) {
			return b.CompareAndSwap(ctx, "held", expected, []byte("new"))
		},
		"delete": func(b *StorageBarrier, expected []byte) (bool, error) {
			return b.CompareAndDelete(ctx, "held", expected)
		},
	}
	for name, op := range ops {
		for _, tc := range []struct {
			guard   string
			applied bool
		}{{guard: "secret-old", applied: true}, {guard: "secret-bad", applied: false}} {
			t.Run(name+"/"+tc.guard, func(t *testing.T) {
				b := newGuardedTestBarrier(t, NewMemoryBackend())
				if err := b.Put(ctx, "held", []byte("secret-old")); err != nil {
					t.Fatalf("Put: %v", err)
				}
				var dropped [][]byte
				b.guardPlaintextDropped = func(p []byte) { dropped = append(dropped, p) }

				applied, err := op(b, []byte(tc.guard))
				if err != nil || applied != tc.applied {
					t.Fatalf("op = (%v, %v), want (%v, nil)", applied, err, tc.applied)
				}
				if len(dropped) != 1 {
					t.Fatalf("decrypted guard copies = %d, want 1", len(dropped))
				}
				if p := dropped[0]; len(p) == 0 || !bytes.Equal(p, make([]byte, len(p))) {
					t.Errorf("decrypted guard copy left unwiped: %q", p)
				}
			})
		}
	}
}

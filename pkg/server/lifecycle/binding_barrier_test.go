package lifecycle_test

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/quiesce"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// xorDatabaseBarrier is a database barrier double: it seals values by
// xor and, like the real one, refuses nothing itself -- the layer refuses
// while IsSealed reports true.
type xorDatabaseBarrier struct{ sealed bool }

func (x *xorDatabaseBarrier) Encrypt(_, p []byte) ([]byte, error) { return xorBytes(p), nil }
func (x *xorDatabaseBarrier) Decrypt(_, c []byte) ([]byte, error) { return xorBytes(c), nil }
func (x *xorDatabaseBarrier) IsSealed() bool                      { return x.sealed }

func xorBytes(in []byte) []byte {
	out := make([]byte, len(in))
	for i, b := range in {
		out[i] = b ^ 0x5A
	}
	return out
}

// TestDatabaseBarrierBinding_ServesTheFullBundleAndRefusesSealed: a
// database with its own barrier materializes through LocalBindingFactory as
// the full bundle, the barrier layered on its store. Before the Manager
// attaches the barrier and while it is sealed, KV, collections, vector and
// the Manager's own backend each refuse as sealed; unsealed, each serves,
// and nothing reaches the store in plaintext.
func TestDatabaseBarrierBinding_ServesTheFullBundleAndRefusesSealed(t *testing.T) {
	for _, backend := range []database.BackendType{database.BackendPebble, database.BackendFile} {
		t.Run(string(backend), func(t *testing.T) {
			testDatabaseBarrierBinding(t, backend)
		})
	}
}

func testDatabaseBarrierBinding(t *testing.T, backend database.BackendType) {
	ctx := context.Background()
	f := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: t.TempDir()}, nil, qrdbconfig.StorageConfig{},
		func(id string) (database.BindingTenant, error) {
			return database.BindingTenant{ID: id, Isolation: tenant.IsolationPrefix}, nil
		})
	cfg := database.DatabaseConfig{Name: "vault", TenantID: "acme", Backend: backend,
		Barrier: &database.DatabaseBarrierConfig{Enabled: true}}
	be, err := f.Create(cfg, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = be.Close() })
	mb, ok := be.(*lifecycle.MaterializedBinding)
	if !ok {
		t.Fatalf("barrier database backend = %T, want *lifecycle.MaterializedBinding", be)
	}
	var _ database.BarrierLayered = mb

	isSealed := func(err error) bool {
		_, ok := errors.AsType[*database.SealedError](err)
		return ok
	}
	// opened reports whether the engine was opened before the seal: its
	// host and vector service then stay, and the layer refuses beneath them.
	refusesAll := func(stage string, opened bool) {
		t.Helper()
		if !mb.IsSealed() {
			t.Fatalf("%s: IsSealed = false", stage)
		}
		if err := mb.Ready(); !opened && !isSealed(err) {
			t.Fatalf("%s: Ready = %v, want *database.SealedError", stage, err)
		}
		if _, err := service.NewKVService(mb.Host(), mb.ShardID()).Get(ctx, "k"); !isSealed(err) {
			t.Fatalf("%s: KV Get = %v, want *database.SealedError", stage, err)
		}
		kv := service.NewKVService(mb.Host(), mb.ShardID())
		if err := kv.Put(ctx, "k", []byte("v")); !isSealed(err) {
			t.Fatalf("%s: KV Put = %v, want *database.SealedError", stage, err)
		}
		if _, err := service.NewCollectionService(kv).CreateCollection(ctx, &service.CreateCollectionRequest{Name: "c2", Database: "vault"}); err == nil {
			t.Fatalf("%s: CreateCollection served while sealed", stage)
		}
		if !opened && mb.Vector() != nil {
			t.Fatalf("%s: vector service served while sealed", stage)
		}
		if _, err := be.Get(ctx, []byte("raw")); !isSealed(err) {
			t.Fatalf("%s: backend Get = %v, want *database.SealedError", stage, err)
		}
		if err := be.Put(ctx, []byte("raw"), []byte("v")); !isSealed(err) {
			t.Fatalf("%s: backend Put = %v, want *database.SealedError", stage, err)
		}
	}
	refusesAll("no barrier attached", false)

	barrier := &xorDatabaseBarrier{}
	mb.LayerBarrier(barrier)
	if err := mb.Ready(); err != nil {
		t.Fatalf("Ready unsealed: %v", err)
	}
	kv := service.NewKVService(mb.Host(), mb.ShardID())
	if err := kv.Put(ctx, "k", []byte("secret-value")); err != nil {
		t.Fatalf("KV Put: %v", err)
	}
	if got, err := kv.Get(ctx, "k"); err != nil || !bytes.Equal(got, []byte("secret-value")) {
		t.Fatalf("KV Get = %q, %v", got, err)
	}
	coll := service.NewCollectionService(kv)
	if _, err := coll.CreateCollection(ctx, &service.CreateCollectionRequest{Name: "people", Database: "vault"}); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if _, err := coll.InsertDocument(ctx, &service.InsertDocumentRequest{Collection: "people", Database: "vault",
		Document: map[string]interface{}{"name": "ada"}}); err != nil {
		t.Fatalf("InsertDocument: %v", err)
	}
	if mb.Vector() == nil {
		t.Fatal("vector service missing from the unsealed bundle")
	}
	if err := be.Put(ctx, []byte("raw"), []byte("plain")); err != nil {
		t.Fatalf("backend Put: %v", err)
	}
	if got, err := be.Get(ctx, []byte("raw")); err != nil || string(got) != "plain" {
		t.Fatalf("backend Get = %q, %v", got, err)
	}

	barrier.sealed = true
	refusesAll("sealed", true)
	barrier.sealed = false
	if got, err := kv.Get(ctx, "k"); err != nil || !bytes.Equal(got, []byte("secret-value")) {
		t.Fatalf("KV Get after unseal = %q, %v", got, err)
	}
}

// gatedDatabaseBarrier is a barrier exposing its seal's quiesce gate, as
// GetBarrier's encryptor does. Armed, its next Encrypt -- which a write makes
// inside the store -- reports it entered and waits for release, so a test
// holds a write in flight.
type gatedDatabaseBarrier struct {
	xorDatabaseBarrier
	gate    quiesce.Gate
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (g *gatedDatabaseBarrier) Quiesce() *quiesce.Gate { return &g.gate }

func (g *gatedDatabaseBarrier) Encrypt(dst, p []byte) ([]byte, error) {
	if g.armed.CompareAndSwap(true, false) {
		g.entered <- struct{}{}
		<-g.release
	}
	return g.xorDatabaseBarrier.Encrypt(dst, p)
}

// TestDatabaseBarrierBinding_WritesHoldTheSealQuiesceLease proves a write to
// a materialized barrier database enters the seal's quiesce lease: a seal
// that closes the gate while the write is in flight cannot drain until the
// write returns, and a write after the gate closed is refused as sealed.
func TestDatabaseBarrierBinding_WritesHoldTheSealQuiesceLease(t *testing.T) {
	for _, backend := range []database.BackendType{database.BackendFile, database.BackendPebble} {
		t.Run(string(backend), func(t *testing.T) {
			ctx := context.Background()
			f := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: t.TempDir()}, nil, qrdbconfig.StorageConfig{},
				func(id string) (database.BindingTenant, error) {
					return database.BindingTenant{ID: id, Isolation: tenant.IsolationPrefix}, nil
				})
			be, err := f.Create(database.DatabaseConfig{Name: "vault", TenantID: "acme", Backend: backend,
				Barrier: &database.DatabaseBarrierConfig{Enabled: true}}, "")
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			t.Cleanup(func() { _ = be.Close() })
			mb := be.(*lifecycle.MaterializedBinding)
			barrier := &gatedDatabaseBarrier{entered: make(chan struct{}), release: make(chan struct{})}
			mb.LayerBarrier(barrier)
			if err := mb.Ready(); err != nil {
				t.Fatalf("Ready: %v", err)
			}

			barrier.armed.Store(true)
			done := make(chan error, 1)
			go func() { done <- be.Put(ctx, []byte("k"), []byte("v")) }()
			<-barrier.entered

			// The seal closes the gate; the write in flight holds the lease.
			barrier.gate.Close()
			var held *quiesce.DrainTimeoutError
			if err := barrier.gate.Drain(0); !errors.As(err, &held) {
				t.Fatalf("Drain with a write in flight = %v, want *quiesce.DrainTimeoutError", err)
			}
			close(barrier.release)
			if err := <-done; err != nil {
				t.Fatalf("in-flight Put = %v, want nil", err)
			}
			if err := barrier.gate.Drain(time.Minute); err != nil {
				t.Fatalf("Drain after the write returned = %v", err)
			}

			// The gate is closed: a write is refused before it reaches the store.
			if err := be.Put(ctx, []byte("k2"), []byte("v")); err == nil {
				t.Fatal("Put after the gate closed = nil, want *database.SealedError")
			} else if _, ok := errors.AsType[*database.SealedError](err); !ok {
				t.Fatalf("Put after the gate closed = %v, want *database.SealedError", err)
			}

			barrier.gate.Reopen()
			if err := be.Put(ctx, []byte("k2"), []byte("v")); err != nil {
				t.Fatalf("Put after reopen = %v", err)
			}
		})
	}
}

// sealSurface is the seal surface a barrier database's verifiable logs use
// (service.databaseSealer): a materialized barrier binding must carry it.
type sealSurface interface {
	SealValue(plaintext []byte) ([]byte, error)
	OpenValue(ciphertext []byte) ([]byte, error)
	Hold() (func(), error)
	IsSealed() bool
}

var _ sealSurface = (*lifecycle.MaterializedBinding)(nil)

// failingDatabaseBarrier is an unsealed barrier whose every encrypt and
// decrypt fails, as a corrupt or foreign ciphertext does.
type failingDatabaseBarrier struct{}

func (failingDatabaseBarrier) Encrypt(_, _ []byte) ([]byte, error) { return nil, errors.New("encrypt") }
func (failingDatabaseBarrier) Decrypt(_, _ []byte) ([]byte, error) { return nil, errors.New("decrypt") }
func (failingDatabaseBarrier) IsSealed() bool                      { return false }

func newSealSurfaceBinding(t *testing.T, barrier bool) *lifecycle.MaterializedBinding {
	t.Helper()
	f := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: t.TempDir()}, nil, qrdbconfig.StorageConfig{},
		func(id string) (database.BindingTenant, error) {
			return database.BindingTenant{ID: id, Isolation: tenant.IsolationPrefix}, nil
		})
	cfg := database.DatabaseConfig{Name: "vault", TenantID: "acme", Backend: database.BackendPebble}
	if barrier {
		cfg.Barrier = &database.DatabaseBarrierConfig{Enabled: true}
	}
	be, err := f.Create(cfg, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = be.Close() })
	return be.(*lifecycle.MaterializedBinding)
}

func wantSealedRefusals(t *testing.T, when string, mb *lifecycle.MaterializedBinding) {
	t.Helper()
	if _, err := mb.SealValue([]byte("p")); !isSealedError(err) {
		t.Errorf("SealValue %s = %v, want *database.SealedError", when, err)
	}
	if _, err := mb.OpenValue([]byte("c")); !isSealedError(err) {
		t.Errorf("OpenValue %s = %v, want *database.SealedError", when, err)
	}
	if release, err := mb.Hold(); !isSealedError(err) {
		if release != nil {
			release()
		}
		t.Errorf("Hold %s = %v, want *database.SealedError", when, err)
	}
}

func isSealedError(err error) bool {
	_, ok := errors.AsType[*database.SealedError](err)
	return ok
}

// TestMaterializedBinding_SealSurface: a barrier binding seals and opens
// values under its database barrier, refuses every seal verb before the
// barrier is attached and once it is sealed, and never passes a ciphertext
// that does not open through; a binding without a barrier refuses them all.
func TestMaterializedBinding_SealSurface(t *testing.T) {
	t.Run("no_barrier", func(t *testing.T) {
		wantSealedRefusals(t, "without a database barrier", newSealSurfaceBinding(t, false))
	})
	t.Run("round_trip_and_sealed", func(t *testing.T) {
		mb := newSealSurfaceBinding(t, true)
		wantSealedRefusals(t, "before attach", mb)

		barrier := &xorDatabaseBarrier{}
		mb.LayerBarrier(barrier)
		ct, err := mb.SealValue([]byte("secret"))
		if err != nil {
			t.Fatalf("SealValue: %v", err)
		}
		if string(ct) == "secret" {
			t.Fatal("SealValue returned the plaintext")
		}
		pt, err := mb.OpenValue(ct)
		if err != nil || string(pt) != "secret" {
			t.Fatalf("OpenValue = %q, %v; want secret", pt, err)
		}
		release, err := mb.Hold()
		if err != nil {
			t.Fatalf("Hold: %v", err)
		}
		release()

		barrier.sealed = true
		if !mb.IsSealed() {
			t.Fatal("IsSealed after seal = false")
		}
		wantSealedRefusals(t, "after seal", mb)
	})
	t.Run("crypto_failures", func(t *testing.T) {
		mb := newSealSurfaceBinding(t, true)
		mb.LayerBarrier(failingDatabaseBarrier{})
		if _, err := mb.SealValue([]byte("p")); err == nil {
			t.Fatal("SealValue with a failing barrier = nil")
		} else if _, ok := errors.AsType[*database.BarrierEncryptError](err); !ok {
			t.Fatalf("SealValue = %v, want *database.BarrierEncryptError", err)
		}
		pt, err := mb.OpenValue([]byte("not a ciphertext"))
		if pt != nil {
			t.Fatalf("OpenValue passed %q through", pt)
		}
		if _, ok := errors.AsType[*database.BarrierDecryptError](err); !ok {
			t.Fatalf("OpenValue = %v, want *database.BarrierDecryptError", err)
		}
	})
	t.Run("seal_waits_for_hold", func(t *testing.T) {
		mb := newSealSurfaceBinding(t, true)
		barrier := &gatedDatabaseBarrier{entered: make(chan struct{}), release: make(chan struct{})}
		mb.LayerBarrier(barrier)
		release, err := mb.Hold()
		if err != nil {
			t.Fatalf("Hold: %v", err)
		}
		barrier.gate.Close()
		var held *quiesce.DrainTimeoutError
		if err := barrier.gate.Drain(0); !errors.As(err, &held) {
			t.Fatalf("Drain with a hold outstanding = %v, want *quiesce.DrainTimeoutError", err)
		}
		if r, err := mb.Hold(); !isSealedError(err) {
			if r != nil {
				r()
			}
			t.Fatalf("Hold after the gate closed = %v, want *database.SealedError", err)
		}
		release()
		if err := barrier.gate.Drain(time.Minute); err != nil {
			t.Fatalf("Drain after release = %v", err)
		}
	})
}

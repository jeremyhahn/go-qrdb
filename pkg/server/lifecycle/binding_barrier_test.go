package lifecycle_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
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

package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/quiesce"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// tagBarrier seals a value as a 0xB0 tag byte followed by the value XORed
// with 0x5A, so a ciphertext differs from its plaintext and a value without
// the tag does not open.
type tagBarrier struct {
	sealed      bool
	failEncrypt bool
	gate        *quiesce.Gate
}

var errTagBarrier = errors.New("tag barrier refused")

func (b *tagBarrier) Encrypt(_, plaintext []byte) ([]byte, error) {
	if b.failEncrypt {
		return nil, errTagBarrier
	}
	out := make([]byte, 0, len(plaintext)+1)
	out = append(out, 0xB0)
	for _, c := range plaintext {
		out = append(out, c^0x5A)
	}
	return out, nil
}

func (b *tagBarrier) Decrypt(_, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) == 0 || ciphertext[0] != 0xB0 {
		return nil, errTagBarrier
	}
	out := make([]byte, 0, len(ciphertext)-1)
	for _, c := range ciphertext[1:] {
		out = append(out, c^0x5A)
	}
	return out, nil
}

func (b *tagBarrier) IsSealed() bool { return b.sealed }

// gatedTagBarrier is a tagBarrier that exposes its seal's quiesce gate.
type gatedTagBarrier struct{ *tagBarrier }

func (b gatedTagBarrier) Quiesce() *quiesce.Gate { return b.gate }

func wantSealed(t *testing.T, verb string, err error) {
	t.Helper()
	if _, ok := errors.AsType[*database.SealedError](err); !ok {
		t.Fatalf("%s = %v, want *database.SealedError", verb, err)
	}
}

func TestDatabaseBarrierStorage_RefusesEveryVerbWithoutAnOpenBarrier(t *testing.T) {
	s := &databaseBarrierStorage{Storage: memory.NewStorage()}
	t.Cleanup(func() { _ = s.Storage.Close() })

	refuse := func(stage string) {
		t.Helper()
		if !s.sealed() {
			t.Fatalf("%s: sealed() = false, want true", stage)
		}
		_, err := s.Get([]byte("a"))
		wantSealed(t, stage+" Get", err)
		wantSealed(t, stage+" Set", s.Set([]byte("a"), []byte("1")))
		wantSealed(t, stage+" Delete", s.Delete([]byte("a")))
		wantSealed(t, stage+" DeleteRange", s.DeleteRange([]byte("a"), []byte("z")))
		_, err = s.List(nil)
		wantSealed(t, stage+" List", err)
		_, err = s.Exists([]byte("a"))
		wantSealed(t, stage+" Exists", err)
		_, err = s.Snapshot()
		wantSealed(t, stage+" Snapshot", err)
		it := s.NewIterator(nil)
		if it.SeekToFirst() {
			t.Fatalf("%s: iterator positioned on a key", stage)
		}
		wantSealed(t, stage+" iterator Error", it.Error())
		_ = it.Close()

		b := s.Batch()
		_, err = b.Get([]byte("a"))
		wantSealed(t, stage+" batch Get", err)
		wantSealed(t, stage+" batch Set", b.Set([]byte("a"), []byte("1")))
		wantSealed(t, stage+" batch Delete", b.Delete([]byte("a")))
		wantSealed(t, stage+" batch DeleteRange", b.DeleteRange([]byte("a"), []byte("z")))
		wantSealed(t, stage+" batch Commit", b.Commit())
		bit := b.NewIterator(nil)
		wantSealed(t, stage+" batch iterator Error", bit.Error())
		_ = bit.Close()
		_ = b.Close()
	}

	refuse("no barrier")
	s.detach() // nothing attached: a no-op

	barrier := &tagBarrier{sealed: true}
	s.attach(barrier)
	refuse("sealed barrier")
}

func TestDatabaseBarrierStorage_SealsWritesAndOpensReads(t *testing.T) {
	s := &databaseBarrierStorage{Storage: memory.NewStorage()}
	t.Cleanup(func() { _ = s.Storage.Close() })
	barrier := &tagBarrier{}
	s.attach(barrier)
	if s.sealed() {
		t.Fatal("sealed() = true with an open barrier")
	}

	for _, kv := range [][2]string{{"a", "1"}, {"b", "2"}, {"c", "3"}} {
		if err := s.Set([]byte(kv[0]), []byte(kv[1])); err != nil {
			t.Fatalf("Set %s: %v", kv[0], err)
		}
	}
	raw, err := s.Storage.Get([]byte("a"))
	if err != nil || bytes.Equal(raw, []byte("1")) || len(raw) != 2 {
		t.Fatalf("stored a = %x, %v; want the sealed value", raw, err)
	}
	if v, err := s.Get([]byte("a")); err != nil || string(v) != "1" {
		t.Fatalf("Get a = %q, %v; want 1", v, err)
	}
	if v, err := s.Get([]byte("missing")); err != nil || v != nil {
		t.Fatalf("Get missing = %q, %v; want nil, nil", v, err)
	}
	if ok, err := s.Exists([]byte("b")); err != nil || !ok {
		t.Fatalf("Exists b = %v, %v; want true", ok, err)
	}
	if keys, err := s.List(nil); err != nil || len(keys) != 3 {
		t.Fatalf("List = %d keys, %v; want 3", len(keys), err)
	}

	it := s.NewIterator(nil)
	var got []string
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		got = append(got, string(it.Value()))
	}
	if err := it.Error(); err != nil {
		t.Fatalf("iterator Error = %v", err)
	}
	_ = it.Close()
	if len(got) != 3 || got[0] != "1" || got[2] != "3" {
		t.Fatalf("iterated values = %q, want [1 2 3]", got)
	}

	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if v, err := snap.Get([]byte("b")); err != nil || string(v) != "2" {
		t.Fatalf("snapshot Get b = %q, %v; want 2", v, err)
	}
	sit := snap.NewIterator(nil)
	if !sit.SeekToFirst() || string(sit.Value()) != "1" {
		t.Fatal("snapshot iterator does not open the first value")
	}
	_ = sit.Close()
	_ = snap.Close()

	if err := s.Delete([]byte("c")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.DeleteRange([]byte("x"), []byte("y")); err != nil {
		t.Fatalf("DeleteRange: %v", err)
	}

	b := s.Batch()
	if err := b.Set([]byte("d"), []byte("4")); err != nil {
		t.Fatalf("batch Set: %v", err)
	}
	if v, err := b.Get([]byte("d")); err != nil || string(v) != "4" {
		t.Fatalf("batch Get d = %q, %v; want 4", v, err)
	}
	bit := b.NewIterator(nil)
	if !bit.SeekToFirst() || string(bit.Value()) != "1" {
		t.Fatal("batch iterator does not open the first value")
	}
	_ = bit.Close()
	if err := b.Delete([]byte("a")); err != nil {
		t.Fatalf("batch Delete: %v", err)
	}
	if err := b.DeleteRange([]byte("x"), []byte("y")); err != nil {
		t.Fatalf("batch DeleteRange: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("batch Commit: %v", err)
	}
	_ = b.Close()
	if v, err := s.Get([]byte("d")); err != nil || string(v) != "4" {
		t.Fatalf("Get d after batch = %q, %v; want 4", v, err)
	}
	if v, err := s.Get([]byte("a")); err != nil || v != nil {
		t.Fatalf("Get a after batch delete = %q, %v; want nil", v, err)
	}
}

func TestDatabaseBarrierStorage_EncryptAndDecryptFailures(t *testing.T) {
	s := &databaseBarrierStorage{Storage: memory.NewStorage()}
	t.Cleanup(func() { _ = s.Storage.Close() })
	barrier := &tagBarrier{}
	s.attach(barrier)

	// A value stored beneath the barrier does not open: Get and the
	// iterator end with a *database.BarrierDecryptError, never the bytes.
	if err := s.Storage.Set([]byte("plain"), []byte("secret")); err != nil {
		t.Fatalf("raw Set: %v", err)
	}
	if _, err := s.Get([]byte("plain")); err == nil {
		t.Fatal("Get of an unsealed value = nil error")
	} else if _, ok := errors.AsType[*database.BarrierDecryptError](err); !ok {
		t.Fatalf("Get of an unsealed value = %v, want *database.BarrierDecryptError", err)
	}
	it := s.NewIterator(nil)
	if !it.SeekToFirst() {
		t.Fatal("iterator SeekToFirst = false, want the raw key")
	}
	if v := it.Value(); v != nil {
		t.Fatalf("iterator Value = %q, want nil", v)
	}
	if v := it.Value(); v != nil {
		t.Fatalf("iterator Value after a decrypt error = %q, want nil", v)
	}
	if it.Valid() {
		t.Fatal("iterator Valid after a decrypt error = true")
	}
	if _, ok := errors.AsType[*database.BarrierDecryptError](it.Error()); !ok {
		t.Fatalf("iterator Error = %v, want *database.BarrierDecryptError", it.Error())
	}
	_ = it.Close()

	barrier.failEncrypt = true
	for verb, err := range map[string]error{
		"Set":       s.Set([]byte("k"), []byte("v")),
		"batch Set": s.Batch().Set([]byte("k"), []byte("v")),
	} {
		if _, ok := errors.AsType[*database.BarrierEncryptError](err); !ok {
			t.Fatalf("%s with a failing encrypt = %v, want *database.BarrierEncryptError", verb, err)
		}
	}
}

func TestDatabaseBarrierStorage_LeaseFollowsTheAttachedGate(t *testing.T) {
	s := &databaseBarrierStorage{Storage: memory.NewStorage()}
	t.Cleanup(func() { _ = s.Storage.Close() })

	first := gatedTagBarrier{&tagBarrier{gate: &quiesce.Gate{}}}
	s.attach(first)
	if err := s.Set([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("Set under the first gate: %v", err)
	}

	// Re-attaching swaps the lease: the first gate's closure no longer
	// refuses this store's writes.
	second := gatedTagBarrier{&tagBarrier{gate: &quiesce.Gate{}}}
	s.attach(second)
	first.gate.Close()
	if err := s.Set([]byte("b"), []byte("2")); err != nil {
		t.Fatalf("Set after the old gate closed: %v", err)
	}
	if err := first.gate.Drain(0); err != nil {
		t.Fatalf("old gate Drain = %v, want nil (its lease is gone)", err)
	}

	second.gate.Close()
	wantSealed(t, "Set with the gate closed", s.Set([]byte("c"), []byte("3")))
	wantSealed(t, "batch Commit with the gate closed", s.Batch().Commit())
	second.gate.Reopen()
	if err := s.Set([]byte("c"), []byte("3")); err != nil {
		t.Fatalf("Set after reopen: %v", err)
	}

	// detach ends the lease: the store admits no more writes.
	s.detach()
	wantSealed(t, "Set after detach", s.Set([]byte("d"), []byte("4")))
}

func TestSealedHost_RefusesEveryCall(t *testing.T) {
	sealed := &database.SealedError{}
	h := sealedHost{id: "acme/vault", err: sealed}
	ctx := context.Background()
	if h.ID() != "acme/vault" || h.RaftAddress() != "" || h.NodeIdentity() != "" {
		t.Fatalf("identity = %q %q %q", h.ID(), h.RaftAddress(), h.NodeIdentity())
	}
	check := func(verb string, err error) {
		t.Helper()
		if !errors.Is(err, sealed) {
			t.Fatalf("%s = %v, want the host's error", verb, err)
		}
	}
	_, err := h.SyncPropose(ctx, 1, nil)
	check("SyncPropose", err)
	_, err = h.SyncRead(ctx, 1, nil)
	check("SyncRead", err)
	_, err = h.ProposeWithMode(ctx, 1, nil, writemode.Mode(0))
	check("ProposeWithMode", err)
	_, err = h.StaleRead(ctx, 1, nil)
	check("StaleRead", err)
	_, err = h.ReadWithConsistency(ctx, 1, nil, consistency.ReadOptions{})
	check("ReadWithConsistency", err)
	_, err = h.FollowerRead(ctx, 1, nil)
	check("FollowerRead", err)
	_, _, err = h.LeaderID(1)
	check("LeaderID", err)
	if err := h.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
}

var _ storage.Storage = (*databaseBarrierStorage)(nil)

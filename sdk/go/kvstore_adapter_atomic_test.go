// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sdk

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// mockGuardedTransportKV embeds mockTransportKV and additionally implements
// transport.GuardedKVClient, modeling every production transport (REST,
// gRPC, QUIC, Unix, MCP, embedded all carry the capability today).
type mockGuardedTransportKV struct {
	*mockTransportKV
	putIfAbsentFn      func(ctx context.Context, key string, value []byte) (bool, error)
	compareAndDeleteFn func(ctx context.Context, key string, expected []byte) (bool, error)
	compareAndSwapFn   func(ctx context.Context, key string, expected, next []byte) (bool, error)
}

func (m *mockGuardedTransportKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	return m.putIfAbsentFn(ctx, key, value)
}

func (m *mockGuardedTransportKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	return m.compareAndDeleteFn(ctx, key, expected)
}

func (m *mockGuardedTransportKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return m.compareAndSwapFn(ctx, key, expected, next)
}

var _ transport.GuardedKVClient = (*mockGuardedTransportKV)(nil)

func newEmptyMockTransportKV() *mockTransportKV {
	return &mockTransportKV{
		putFn:    func(context.Context, string, []byte, ...transport.WriteOption) error { return nil },
		getFn:    func(context.Context, string, ...transport.ReadOption) ([]byte, error) { return nil, nil },
		deleteFn: func(context.Context, string, ...transport.WriteOption) error { return nil },
		listFn: func(context.Context, string, ...transport.ReadOption) ([]string, error) {
			return nil, nil
		},
		scanFn: func(context.Context, string, ...transport.ReadOption) (map[string][]byte, error) {
			return nil, nil
		},
		existsFn: func(context.Context, string, ...transport.ReadOption) (bool, error) { return false, nil },
	}
}

// TestKVStoreAdapter_AtomicKVStore_Forwards proves kvStoreAdapter forwards
// PutIfAbsent/CompareAndDelete/CompareAndSwap to the transport's
// GuardedKVClient capability (go-qrdb#589): before this, every SDK-driven
// DAO's SaveIfAbsent was refused with AtomicWriteUnsupportedError on every
// transport, because kvStoreAdapter implemented only kvstore.KVStore.
func TestKVStoreAdapter_AtomicKVStore_Forwards(t *testing.T) {
	var sawPutIfAbsent, sawCAD, sawCAS bool
	guarded := &mockGuardedTransportKV{
		mockTransportKV: newEmptyMockTransportKV(),
		putIfAbsentFn: func(_ context.Context, key string, value []byte) (bool, error) {
			sawPutIfAbsent = true
			if key != "k" || string(value) != "v" {
				t.Errorf("PutIfAbsent(%q, %q), want (k, v)", key, value)
			}
			return true, nil
		},
		compareAndDeleteFn: func(_ context.Context, key string, expected []byte) (bool, error) {
			sawCAD = true
			if key != "k" || string(expected) != "v" {
				t.Errorf("CompareAndDelete(%q, %q), want (k, v)", key, expected)
			}
			return true, nil
		},
		compareAndSwapFn: func(_ context.Context, key string, expected, next []byte) (bool, error) {
			sawCAS = true
			if key != "k" || string(expected) != "v" || string(next) != "v2" {
				t.Errorf("CompareAndSwap(%q, %q, %q), want (k, v, v2)", key, expected, next)
			}
			return true, nil
		},
	}

	adapter, err := NewKVStoreAdapter(guarded, &mockTransportIndex{})
	if err != nil {
		t.Fatalf("NewKVStoreAdapter: %v", err)
	}
	atomicStore, ok := adapter.(kvstore.AtomicKVStore)
	if !ok {
		t.Fatal("kvStoreAdapter does not implement kvstore.AtomicKVStore")
	}

	ctx := context.Background()
	if stored, err := atomicStore.PutIfAbsent(ctx, "k", []byte("v")); err != nil || !stored {
		t.Fatalf("PutIfAbsent = (%v, %v), want (true, nil)", stored, err)
	}
	if swapped, err := atomicStore.CompareAndSwap(ctx, "k", []byte("v"), []byte("v2")); err != nil || !swapped {
		t.Fatalf("CompareAndSwap = (%v, %v), want (true, nil)", swapped, err)
	}
	if deleted, err := atomicStore.CompareAndDelete(ctx, "k", []byte("v")); err != nil || !deleted {
		t.Fatalf("CompareAndDelete = (%v, %v), want (true, nil)", deleted, err)
	}
	if !sawPutIfAbsent || !sawCAD || !sawCAS {
		t.Fatalf("forwarding reached: PutIfAbsent=%v CompareAndDelete=%v CompareAndSwap=%v, want all true",
			sawPutIfAbsent, sawCAD, sawCAS)
	}
}

// TestKVStoreAdapter_AtomicKVStore_RefusesWhenTransportLacksTheCapability
// proves the adapter still satisfies kvstore.AtomicKVStore when the
// underlying transport.KVClient is NOT a GuardedKVClient, reporting
// kvstore.ErrAtomicWriteUnsupported from the call rather than failing the
// type assertion -- the "has the method but not the capability" case
// kvstore.AtomicKVStore's own doc anticipates, and the same refusal
// pkg/dao.GenericDAO.SaveIfAbsent already treats identically to the
// assertion itself failing.
func TestKVStoreAdapter_AtomicKVStore_RefusesWhenTransportLacksTheCapability(t *testing.T) {
	plain := newEmptyMockTransportKV()
	adapter, err := NewKVStoreAdapter(plain, &mockTransportIndex{})
	if err != nil {
		t.Fatalf("NewKVStoreAdapter: %v", err)
	}
	atomicStore, ok := adapter.(kvstore.AtomicKVStore)
	if !ok {
		t.Fatal("kvStoreAdapter does not implement kvstore.AtomicKVStore")
	}

	ctx := context.Background()
	if _, err := atomicStore.PutIfAbsent(ctx, "k", []byte("v")); !errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
		t.Errorf("PutIfAbsent error = %v, want ErrAtomicWriteUnsupported", err)
	}
	if _, err := atomicStore.CompareAndDelete(ctx, "k", []byte("v")); !errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
		t.Errorf("CompareAndDelete error = %v, want ErrAtomicWriteUnsupported", err)
	}
	if _, err := atomicStore.CompareAndSwap(ctx, "k", []byte("v"), []byte("v2")); !errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
		t.Errorf("CompareAndSwap error = %v, want ErrAtomicWriteUnsupported", err)
	}
}

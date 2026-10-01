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

package seal

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// fencedBackends returns every in-package kvstore.FencedCreator backend
// together with a raw writer for seeding keys, so each case below runs against
// all of them.
func fencedBackends() map[string]struct {
	fc   kvstore.FencedCreator
	seed func(key string, value []byte)
	raw  func(key string) []byte
} {
	mem := NewMemoryBackend()
	legacy := newMockLegacyStorage()
	adapter := NewStorageAdapter(legacy).(*StorageAdapter)
	return map[string]struct {
		fc   kvstore.FencedCreator
		seed func(key string, value []byte)
		raw  func(key string) []byte
	}{
		"MemoryBackend": {
			fc:   mem,
			seed: func(k string, v []byte) { _ = mem.Put(context.Background(), k, v) },
			raw:  func(k string) []byte { v, _ := mem.Get(context.Background(), k); return v },
		},
		"StorageAdapter": {
			fc:   adapter,
			seed: func(k string, v []byte) { legacy.data[k] = v },
			raw:  func(k string) []byte { return legacy.data[k] },
		},
	}
}

func TestPutIfAllAbsent_Backends_CreatesWhenAllAbsent(t *testing.T) {
	for name, b := range fencedBackends() {
		t.Run(name, func(t *testing.T) {
			created, err := b.fc.PutIfAllAbsent(context.Background(), "meta/acme", []byte("v"), "tomb/acme")
			if err != nil || !created {
				t.Fatalf("PutIfAllAbsent = %v, %v; want true, nil", created, err)
			}
			if !bytes.Equal(b.raw("meta/acme"), []byte("v")) {
				t.Fatal("the key was not written")
			}
		})
	}
}

func TestPutIfAllAbsent_Backends_RefusedByAPresentFence(t *testing.T) {
	for name, b := range fencedBackends() {
		t.Run(name, func(t *testing.T) {
			b.seed("tomb/acme", []byte("retired"))
			created, err := b.fc.PutIfAllAbsent(context.Background(), "meta/acme", []byte("v"), "tomb/acme")
			if err != nil || created {
				t.Fatalf("PutIfAllAbsent = %v, %v; want false, nil", created, err)
			}
			if b.raw("meta/acme") != nil {
				t.Fatal("a refused fenced create wrote the key")
			}
		})
	}
}

func TestPutIfAllAbsent_Backends_RefusedByAPresentKey(t *testing.T) {
	for name, b := range fencedBackends() {
		t.Run(name, func(t *testing.T) {
			b.seed("meta/acme", []byte("winner"))
			created, err := b.fc.PutIfAllAbsent(context.Background(), "meta/acme", []byte("loser"))
			if err != nil || created {
				t.Fatalf("PutIfAllAbsent = %v, %v; want false, nil", created, err)
			}
			if !bytes.Equal(b.raw("meta/acme"), []byte("winner")) {
				t.Fatal("a refused fenced create overwrote the key")
			}
		})
	}
}

func TestStorageAdapter_PutIfAllAbsent_PropagatesStorageErrors(t *testing.T) {
	inner := newMockLegacyStorage()
	inner.existErr = errors.New("exists failed")
	if _, err := NewStorageAdapter(inner).(*StorageAdapter).PutIfAllAbsent(context.Background(), "k", []byte("v"), "f"); err == nil {
		t.Fatal("an Exists failure must be reported, not read as absent")
	}
	inner = newMockLegacyStorage()
	inner.setErr = errors.New("set failed")
	if _, err := NewStorageAdapter(inner).(*StorageAdapter).PutIfAllAbsent(context.Background(), "k", []byte("v")); err == nil {
		t.Fatal("a Set failure must be reported")
	}
}

func TestStorageBarrier_PutIfAllAbsent_Sealed(t *testing.T) {
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), BarrierConfig{RootKeyPath: "test/root-key"}, testSoftwareStrategy(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.PutIfAllAbsent(context.Background(), "k", []byte("v"), "f"); !errors.Is(err, ErrSealed) {
		t.Fatalf("PutIfAllAbsent on a sealed barrier = %v; want ErrSealed", err)
	}
}

// TestStorageBarrier_PutIfAllAbsent_EncryptsAndHonoursTheFence: the value
// round-trips through the barrier, and a present fence refuses the create.
func TestStorageBarrier_PutIfAllAbsent_EncryptsAndHonoursTheFence(t *testing.T) {
	b := newUnsealedStorageBarrier(t)
	ctx := context.Background()

	created, err := b.PutIfAllAbsent(ctx, "meta/acme", []byte("secret"), "tomb/acme")
	if err != nil || !created {
		t.Fatalf("PutIfAllAbsent = %v, %v; want true, nil", created, err)
	}
	got, err := b.Get(ctx, "meta/acme")
	if err != nil || !bytes.Equal(got, []byte("secret")) {
		t.Fatalf("Get = %q, %v; want the plaintext back", got, err)
	}
	stored, err := b.base.Get(ctx, "meta/acme")
	if err != nil || bytes.Equal(stored, []byte("secret")) {
		t.Fatal("the value reached the base backend in plaintext")
	}

	if err := b.Put(ctx, "tomb/beta", []byte("retired")); err != nil {
		t.Fatal(err)
	}
	created, err = b.PutIfAllAbsent(ctx, "meta/beta", []byte("x"), "tomb/beta")
	if err != nil || created {
		t.Fatalf("PutIfAllAbsent past a present fence = %v, %v; want false, nil", created, err)
	}
}

// unfencedMemory hides MemoryBackend's fenced create.
type unfencedMemory struct{ StorageBackend }

// TestStorageBarrier_PutIfAllAbsent_RefusesABaseWithoutTheCapability pins that
// the barrier never degrades to a check followed by a write.
func TestStorageBarrier_PutIfAllAbsent_RefusesABaseWithoutTheCapability(t *testing.T) {
	b := newUnsealedStorageBarrier(t)
	b.base = unfencedMemory{b.base}
	if _, err := b.PutIfAllAbsent(context.Background(), "k", []byte("v"), "f"); !errors.Is(err, ErrFencedCreateUnsupported) {
		t.Fatalf("PutIfAllAbsent over an unfenced base = %v; want ErrFencedCreateUnsupported", err)
	}
}

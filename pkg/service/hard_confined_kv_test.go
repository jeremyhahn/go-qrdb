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

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// fakeKV is a minimal, map-backed KVServicer double. It embeds the
// interface as nil so any method this test file does not implement
// panics loudly if a hard-confined refusal fails to short-circuit before
// delegating -- the same "unimplemented methods panic" shape the
// project's existing stub*Service doubles use (see pkg/server's
// server_test.go).
type fakeKV struct {
	KVServicer
	store map[string][]byte
}

func newFakeKV() *fakeKV { return &fakeKV{store: map[string][]byte{}} }

func (f *fakeKV) Put(_ context.Context, key string, value []byte) error {
	f.store[key] = value
	return nil
}

func (f *fakeKV) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	if _, ok := f.store[key]; ok {
		return false, nil
	}
	f.store[key] = value
	return true, nil
}

func (f *fakeKV) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	if string(f.store[key]) != string(expected) {
		return false, nil
	}
	delete(f.store, key)
	return true, nil
}

func (f *fakeKV) CompareAndSwap(_ context.Context, key string, expected, next []byte) (bool, error) {
	if string(f.store[key]) != string(expected) {
		return false, nil
	}
	f.store[key] = next
	return true, nil
}

func (f *fakeKV) Get(_ context.Context, key string) ([]byte, error) { return f.store[key], nil }

func (f *fakeKV) Delete(_ context.Context, key string) error {
	delete(f.store, key)
	return nil
}

func (f *fakeKV) List(_ context.Context, prefix string) ([]string, error) {
	var out []string
	for k := range f.store {
		if len(prefix) == 0 || (len(k) >= len(prefix) && k[:len(prefix)] == prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}

func (f *fakeKV) ListAll(ctx context.Context, prefix string) ([]string, error) {
	return f.List(ctx, prefix)
}

func (f *fakeKV) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	out := make(map[string][]byte)
	for k, v := range f.store {
		if len(prefix) == 0 || (len(k) >= len(prefix) && k[:len(prefix)] == prefix) {
			out[k] = v
		}
	}
	return out, nil
}

func (f *fakeKV) GetStale(ctx context.Context, key string) ([]byte, error) { return f.Get(ctx, key) }

func (f *fakeKV) ListStale(ctx context.Context, prefix string) ([]string, error) {
	return f.List(ctx, prefix)
}

func (f *fakeKV) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	return f.Scan(ctx, prefix)
}

func (f *fakeKV) PutWithMode(ctx context.Context, key string, value []byte, _ writemode.Mode) error {
	return f.Put(ctx, key, value)
}

func (f *fakeKV) DeleteWithMode(ctx context.Context, key string, _ writemode.Mode) error {
	return f.Delete(ctx, key)
}

func (f *fakeKV) GetWithConsistency(ctx context.Context, key string, _ consistency.ReadOptions) ([]byte, error) {
	return f.Get(ctx, key)
}

func (f *fakeKV) ListWithConsistency(ctx context.Context, prefix string, _ consistency.ReadOptions) ([]string, error) {
	return f.List(ctx, prefix)
}

func (f *fakeKV) ScanWithConsistency(ctx context.Context, prefix string, _ consistency.ReadOptions) (map[string][]byte, error) {
	return f.Scan(ctx, prefix)
}

func (f *fakeKV) ExistsWithConsistency(_ context.Context, key string, _ consistency.ReadOptions) (bool, error) {
	_, ok := f.store[key]
	return ok, nil
}

const confinedKey = hardConfinedPrefix + "material"

func TestNewHardConfinedKV_NilServicePassesThroughNil(t *testing.T) {
	if got := NewHardConfinedKV(nil); got != nil {
		t.Fatalf("NewHardConfinedKV(nil) = %v, want nil", got)
	}
}

func TestHardConfinedKV_PointKeyMethodsRefuseTheConfinedKey(t *testing.T) {
	ctx := context.Background()
	fake := newFakeKV()
	fake.store[confinedKey] = []byte("the cluster CA's private key")
	kv := NewHardConfinedKV(fake)

	assertConfined := func(t *testing.T, err error) {
		t.Helper()
		var confErr *HardConfinedKeyError
		if !errors.As(err, &confErr) {
			t.Fatalf("error = %v (%T), want *HardConfinedKeyError", err, err)
		}
	}

	if _, err := kv.Get(ctx, confinedKey); err == nil {
		t.Fatal("Get succeeded for the confined key, want a refusal")
	} else {
		assertConfined(t, err)
	}
	if err := kv.Put(ctx, confinedKey, []byte("overwrite")); err == nil {
		t.Fatal("Put succeeded for the confined key, want a refusal")
	} else {
		assertConfined(t, err)
	}
	if _, err := kv.PutIfAbsent(ctx, confinedKey, []byte("x")); err == nil {
		t.Fatal("PutIfAbsent succeeded for the confined key, want a refusal")
	} else {
		assertConfined(t, err)
	}
	if _, err := kv.CompareAndSwap(ctx, confinedKey, fake.store[confinedKey], []byte("x")); err == nil {
		t.Fatal("CompareAndSwap succeeded for the confined key, want a refusal")
	} else {
		assertConfined(t, err)
	}
	if _, err := kv.CompareAndDelete(ctx, confinedKey, fake.store[confinedKey]); err == nil {
		t.Fatal("CompareAndDelete succeeded for the confined key, want a refusal")
	} else {
		assertConfined(t, err)
	}
	if err := kv.Delete(ctx, confinedKey); err == nil {
		t.Fatal("Delete succeeded for the confined key, want a refusal")
	} else {
		assertConfined(t, err)
	}
	if _, err := kv.GetStale(ctx, confinedKey); err == nil {
		t.Fatal("GetStale succeeded for the confined key, want a refusal")
	} else {
		assertConfined(t, err)
	}
	if err := kv.PutWithMode(ctx, confinedKey, []byte("x"), writemode.Sync); err == nil {
		t.Fatal("PutWithMode succeeded for the confined key, want a refusal")
	} else {
		assertConfined(t, err)
	}
	if err := kv.DeleteWithMode(ctx, confinedKey, writemode.Sync); err == nil {
		t.Fatal("DeleteWithMode succeeded for the confined key, want a refusal")
	} else {
		assertConfined(t, err)
	}
	if _, err := kv.GetWithConsistency(ctx, confinedKey, consistency.ReadOptions{}); err == nil {
		t.Fatal("GetWithConsistency succeeded for the confined key, want a refusal")
	} else {
		assertConfined(t, err)
	}
	if _, err := kv.ExistsWithConsistency(ctx, confinedKey, consistency.ReadOptions{}); err == nil {
		t.Fatal("ExistsWithConsistency succeeded for the confined key, want a refusal")
	} else {
		assertConfined(t, err)
	}

	// None of the refusals above may have reached the underlying store:
	// the value must be exactly what it started as.
	if string(fake.store[confinedKey]) != "the cluster CA's private key" {
		t.Fatalf("confined key's value changed to %q despite every write being refused", fake.store[confinedKey])
	}
}

func TestHardConfinedKV_EnumerationMethodsRefuseTheConfinedPrefix(t *testing.T) {
	ctx := context.Background()
	fake := newFakeKV()
	fake.store[confinedKey] = []byte("secret")
	kv := NewHardConfinedKV(fake)

	if _, err := kv.List(ctx, hardConfinedPrefix); err == nil {
		t.Fatal("List(hardConfinedPrefix) succeeded, want a refusal")
	}
	if _, err := kv.Scan(ctx, hardConfinedPrefix); err == nil {
		t.Fatal("Scan(hardConfinedPrefix) succeeded, want a refusal")
	}
	if _, err := kv.ListStale(ctx, hardConfinedPrefix); err == nil {
		t.Fatal("ListStale(hardConfinedPrefix) succeeded, want a refusal")
	}
	if _, err := kv.ScanStale(ctx, hardConfinedPrefix); err == nil {
		t.Fatal("ScanStale(hardConfinedPrefix) succeeded, want a refusal")
	}
	if _, err := kv.ListWithConsistency(ctx, hardConfinedPrefix, consistency.ReadOptions{}); err == nil {
		t.Fatal("ListWithConsistency(hardConfinedPrefix) succeeded, want a refusal")
	}
	if _, err := kv.ScanWithConsistency(ctx, hardConfinedPrefix, consistency.ReadOptions{}); err == nil {
		t.Fatal("ScanWithConsistency(hardConfinedPrefix) succeeded, want a refusal")
	}
}

func TestHardConfinedKV_EnumerationMethodsFilterTheConfinedKeyFromABroaderSweep(t *testing.T) {
	ctx := context.Background()
	fake := newFakeKV()
	fake.store[confinedKey] = []byte("secret")
	fake.store[InternalKeyPrefixSystem+"roles/x"] = []byte(`{"name":"x"}`)
	kv := NewHardConfinedKV(fake)

	// A sweep broader than the confined namespace (here, everything) must
	// not surface the confined key, even though the fake's own List/Scan
	// apply no filtering of their own -- the decorator is the only guard
	// in this test, proving it does not depend on the wrapped servicer
	// also filtering.
	keys, err := kv.List(ctx, "")
	if err != nil {
		t.Fatalf("List(\"\"): %v", err)
	}
	for _, k := range keys {
		if k == confinedKey {
			t.Fatalf("List(\"\") returned the confined key %q", k)
		}
	}
	if len(keys) != 1 || keys[0] != InternalKeyPrefixSystem+"roles/x" {
		t.Fatalf("List(\"\") = %v, want exactly the non-confined key", keys)
	}

	data, err := kv.Scan(ctx, "")
	if err != nil {
		t.Fatalf("Scan(\"\"): %v", err)
	}
	if _, ok := data[confinedKey]; ok {
		t.Fatal("Scan(\"\") returned the confined key")
	}
	if len(data) != 1 {
		t.Fatalf("Scan(\"\") = %v, want exactly the non-confined key", data)
	}

	allKeys, err := kv.ListAll(ctx, "")
	if err != nil {
		t.Fatalf("ListAll(\"\"): %v", err)
	}
	for _, k := range allKeys {
		if k == confinedKey {
			t.Fatal("ListAll(\"\") returned the confined key despite ListAll's own filtering override")
		}
	}
}

func TestHardConfinedKV_OrdinaryKeysPassThroughUnaffected(t *testing.T) {
	ctx := context.Background()
	fake := newFakeKV()
	kv := NewHardConfinedKV(fake)

	if err := kv.Put(ctx, "ordinary/key", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := kv.Get(ctx, "ordinary/key")
	if err != nil || string(got) != "v" {
		t.Fatalf("Get = %q, %v; want %q, nil", got, err, "v")
	}
	if ok, err := kv.PutIfAbsent(ctx, "ordinary/other", []byte("w")); err != nil || !ok {
		t.Fatalf("PutIfAbsent = %v, %v; want true, nil", ok, err)
	}
	if swapped, err := kv.CompareAndSwap(ctx, "ordinary/key", []byte("v"), []byte("v2")); err != nil || !swapped {
		t.Fatalf("CompareAndSwap = %v, %v; want true, nil", swapped, err)
	}
	if deleted, err := kv.CompareAndDelete(ctx, "ordinary/other", []byte("w")); err != nil || !deleted {
		t.Fatalf("CompareAndDelete = %v, %v; want true, nil", deleted, err)
	}
	if err := kv.Delete(ctx, "ordinary/key"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

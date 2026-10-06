// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"bytes"
	"context"
	"errors"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
)

// fakeKV is an in-memory KVServicer double. It is deliberately independent
// of kvstore.MemoryKVStore: this package must not import pkg/kvstore just
// for a test fake, and the two interfaces are not identical (KVServicer's
// Scan returns a map, not a callback).
type fakeKV struct {
	mu   sync.Mutex
	data map[string][]byte

	// failGet, when set, is returned by every Get instead of a real
	// lookup -- the "a failingBackend on the replicated store" fault seam
	// engineering.md's test strategy calls for.
	failGet error

	// failScan, when set, is returned by every Scan instead of a real
	// scan.
	failScan error
}

func newFakeKV() *fakeKV { return &fakeKV{data: make(map[string][]byte)} }

func (f *fakeKV) Get(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failGet != nil {
		return nil, f.failGet
	}
	v, ok := f.data[key]
	if !ok {
		return nil, nil
	}
	return bytes.Clone(v), nil
}

func (f *fakeKV) Put(_ context.Context, key string, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[key] = bytes.Clone(value)
	return nil
}

func (f *fakeKV) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.data[key]; exists {
		return false, nil
	}
	f.data[key] = bytes.Clone(value)
	return true, nil
}

func (f *fakeKV) CompareAndSwap(_ context.Context, key string, expected, next []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.data[key]
	if !ok || !bytes.Equal(cur, expected) {
		return false, nil
	}
	f.data[key] = bytes.Clone(next)
	return true, nil
}

func (f *fakeKV) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.data[key]
	if !ok || !bytes.Equal(cur, expected) {
		return false, nil
	}
	delete(f.data, key)
	return true, nil
}

func (f *fakeKV) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failScan != nil {
		return nil, f.failScan
	}
	out := make(map[string][]byte)
	for k, v := range f.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			out[k] = bytes.Clone(v)
		}
	}
	return out, nil
}

// fakeTx is an in-memory TransactionServicer double that evaluates
// lock.Compare against a shared fakeKV, tracking a per-key version that
// increments on every write and resets to 0 on delete -- exactly the
// "version 0 means absent" convention
// lifecycle.kvSealBackend.PutIfAbsent documents for the real engine. It
// supports CompareVersion/CompareEqual and CompareValue/CompareEqual,
// which is everything Catalog's Reserve and Release use.
type fakeTx struct {
	kv *fakeKV

	mu       sync.Mutex
	versions map[string]int64

	// forceFailures, when > 0, makes the next N calls to Execute report
	// Succeeded=false without evaluating anything -- the bounded-retry
	// fault seam engineering.md's test strategy calls for ("reservation
	// CAS under concurrent creates").
	forceFailures int
}

func newFakeTx(kv *fakeKV) *fakeTx {
	return &fakeTx{kv: kv, versions: make(map[string]int64)}
}

var errUnsupportedCompare = errors.New("fakeTx: unsupported compare target in test double")

func (t *fakeTx) Execute(ctx context.Context, compares []lock.Compare, successOps, failureOps []lock.Op) (*lock.TransactionResponse, error) {
	t.mu.Lock()
	if t.forceFailures > 0 {
		t.forceFailures--
		t.mu.Unlock()
		return &lock.TransactionResponse{Succeeded: false}, nil
	}
	t.mu.Unlock()

	for _, c := range compares {
		key := string(c.Key)
		ok, err := t.evalCompare(ctx, c, key)
		if err != nil {
			return nil, err
		}
		if !ok {
			ops := failureOps
			if err := t.apply(ctx, ops); err != nil {
				return nil, err
			}
			return &lock.TransactionResponse{Succeeded: false}, nil
		}
	}
	if err := t.apply(ctx, successOps); err != nil {
		return nil, err
	}
	return &lock.TransactionResponse{Succeeded: true}, nil
}

func (t *fakeTx) evalCompare(ctx context.Context, c lock.Compare, key string) (bool, error) {
	switch c.Target {
	case lock.CompareVersion:
		t.mu.Lock()
		v := t.versions[key]
		t.mu.Unlock()
		return v == c.Version, nil
	case lock.CompareValue:
		cur, err := t.kv.Get(ctx, key)
		if err != nil {
			return false, err
		}
		return bytes.Equal(cur, c.Value), nil
	default:
		return false, errUnsupportedCompare
	}
}

func (t *fakeTx) apply(ctx context.Context, ops []lock.Op) error {
	for _, op := range ops {
		key := string(op.Key)
		switch op.Type {
		case lock.OpPut:
			if err := t.kv.Put(ctx, key, op.Value); err != nil {
				return err
			}
			t.mu.Lock()
			t.versions[key]++
			t.mu.Unlock()
		case lock.OpDelete:
			t.kv.mu.Lock()
			delete(t.kv.data, key)
			t.kv.mu.Unlock()
			t.mu.Lock()
			t.versions[key] = 0
			t.mu.Unlock()
		default:
			return errUnsupportedCompare
		}
	}
	return nil
}

// fakeRegistry is a minimal Registry double so catalog tests do not need a
// real dir provider; it only needs to answer Lookup for CreatePool's
// provider validation.
type fakeRegistry struct {
	known map[string]struct{}
}

func newFakeRegistry(names ...string) *fakeRegistry {
	r := &fakeRegistry{known: make(map[string]struct{})}
	for _, n := range names {
		r.known[n] = struct{}{}
	}
	return r
}

func (r *fakeRegistry) Register(p Provider) error {
	r.known[p.Name()] = struct{}{}
	return nil
}

func (r *fakeRegistry) Lookup(name string) (Provider, error) {
	if _, ok := r.known[name]; !ok {
		return nil, &ProviderNotRegisteredError{Provider: name}
	}
	return NewDirProvider(), nil
}

func (r *fakeRegistry) ListAvailable() []string {
	names := make([]string, 0, len(r.known))
	for n := range r.known {
		names = append(names, n)
	}
	return names
}

// newTestCatalog builds a Catalog over fresh fakes, pre-registered with
// the "dir" provider.
func newTestCatalog() (*Catalog, *fakeKV, *fakeTx) {
	kv := newFakeKV()
	tx := newFakeTx(kv)
	registry := newFakeRegistry("dir")
	cat, err := NewCatalog(kv, tx, registry)
	if err != nil {
		panic(err) // test setup only; a failure here is a defect in the fakes
	}
	return cat, kv, tx
}

// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// knownGoodKV is a KVStore holding one value per key, with injectable faults
// on Get and Put.
type knownGoodKV struct {
	kvstore.KVStore
	data           map[string][]byte
	getErr, putErr error
}

func (k *knownGoodKV) Get(_ context.Context, key string) ([]byte, error) {
	if k.getErr != nil {
		return nil, k.getErr
	}
	return k.data[key], nil
}

func (k *knownGoodKV) Put(_ context.Context, key string, value []byte) error {
	if k.putErr != nil {
		return k.putErr
	}
	if k.data == nil {
		k.data = map[string][]byte{}
	}
	k.data[key] = value
	return nil
}

// TestSOKnownGoodStore_RoundTripAndFaults: the set round-trips through the KV
// store under the tenant's key; an absent key is empty; a corrupt record and
// store failures are *OperationError.
func TestSOKnownGoodStore_RoundTripAndFaults(t *testing.T) {
	kv := &knownGoodKV{}
	s := soKnownGoodStore{store: kv}
	if got, err := s.LoadKnownGood(context.Background(), "acme"); err != nil || got != nil {
		t.Fatalf("absent = %v, %v", got, err)
	}
	if err := s.SaveKnownGood(context.Background(), "acme", []string{"id:acme/so"}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LoadKnownGood(context.Background(), "acme"); err != nil || len(got) != 1 || got[0] != "id:acme/so" {
		t.Fatalf("reloaded = %v, %v", got, err)
	}
	var opErr *OperationError
	kv.data[soKnownGoodPrefix+"acme"] = []byte("{")
	if _, err := s.LoadKnownGood(context.Background(), "acme"); !errors.As(err, &opErr) {
		t.Fatalf("corrupt = %v", err)
	}
	kv.getErr = errors.New("raft down")
	if _, err := s.LoadKnownGood(context.Background(), "acme"); !errors.As(err, &opErr) {
		t.Fatalf("get failure = %v", err)
	}
	kv.putErr = errors.New("raft down")
	if err := s.SaveKnownGood(context.Background(), "acme", nil); !errors.As(err, &opErr) {
		t.Fatalf("put failure = %v", err)
	}
}

// failingKnownGood is a SOPINKnownGoodStore that cannot read or write.
type failingKnownGood struct{}

func (failingKnownGood) LoadKnownGood(context.Context, string) ([]string, error) {
	return nil, errors.New("store down")
}
func (failingKnownGood) SaveKnownGood(context.Context, string, []string) error {
	return errors.New("store down")
}

// TestSOPINLockout_KnownGoodStoreFaultsNeverRefuseAGoodPIN: a store that
// cannot load leaves the set to be loaded on a later check, and one that
// cannot save keeps the exemption in memory; neither turns a correct PIN into
// a refusal.
func TestSOPINLockout_KnownGoodStoreFaultsNeverRefuseAGoodPIN(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := NewSOPINLockout(func() time.Time { return now }, WithKnownGoodStore(failingKnownGood{}))
	right := func() error { return nil }
	never := func(error) bool { return false }
	if err := l.Check(context.Background(), "node", "id:system/officer", right, never); err != nil {
		t.Fatalf("check with a failing store = %v", err)
	}
	state := l.keys["node"]
	if state == nil || !state.isKnownGood(knownGoodDigest("node", "id:system/officer")) || state.knownGoodLoaded || !state.nextLoad.After(now) {
		t.Fatalf("state = %+v, want the exemption in memory and the load retried later", state)
	}
}

// countingStore counts loads and fails every one.
type countingStore struct{ loads int }

func (c *countingStore) LoadKnownGood(context.Context, string) ([]string, error) {
	c.loads++
	return nil, errors.New("store down")
}
func (c *countingStore) SaveKnownGood(context.Context, string, []string) error { return nil }

// TestSOPINLockout_FailingLoadIsNotRetriedEveryCheck: a load that failed is
// retried only after SOPINKnownGoodRetry, not on every check. The first
// check makes two reads -- its load, and the read its save merges with (the
// principal became known-good) -- and the save, its read failing, writes
// nothing; later checks of the same principal change nothing and read
// nothing until the retry interval has passed.
func TestSOPINLockout_FailingLoadIsNotRetriedEveryCheck(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	store := &countingStore{}
	l := NewSOPINLockout(func() time.Time { return now }, WithKnownGoodStore(store))
	right := func() error { return nil }
	never := func(error) bool { return false }
	for i := 0; i < 5; i++ {
		_ = l.Check(context.Background(), "node", "id:system/officer", right, never)
	}
	if store.loads != 2 {
		t.Fatalf("loads = %d within the retry interval, want 2 (the load and the save's read)", store.loads)
	}
	now = now.Add(SOPINKnownGoodRetry)
	_ = l.Check(context.Background(), "node", "id:system/officer", right, never)
	if store.loads != 3 {
		t.Fatalf("loads = %d after the retry interval, want 3", store.loads)
	}
}

// blockingStore blocks every call until release is closed, and reports each
// call's start on entered.
type blockingStore struct {
	entered chan string
	release chan struct{}
}

func (b *blockingStore) LoadKnownGood(ctx context.Context, key string) ([]string, error) {
	b.entered <- "load " + key
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return nil, nil
}

func (b *blockingStore) SaveKnownGood(ctx context.Context, key string, _ []string) error {
	b.entered <- "save " + key
	select {
	case <-b.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// TestSOPINLockout_SlowStoreHoldsUpOnlyItsOwnCheck: a known-good store call
// that does not return -- a degraded Raft shard -- holds up the check that
// made it, never another key's check: no store call runs under the
// lockout's lock. Both a stuck load and a stuck save are covered.
func TestSOPINLockout_SlowStoreHoldsUpOnlyItsOwnCheck(t *testing.T) {
	for _, stuck := range []string{"load", "save"} {
		t.Run(stuck, func(t *testing.T) {
			store := &blockingStore{entered: make(chan string, 4), release: make(chan struct{})}
			l := NewSOPINLockout(nil, WithKnownGoodStore(store))
			right := func() error { return nil }
			never := func(error) bool { return false }
			done := make(chan struct{})
			go func() {
				defer close(done)
				_ = l.Check(context.Background(), "acme", "id:acme/so", right, never)
			}()
			// The acme check's load blocks; for the save case let the load
			// and the save's merge read through, and wait for the write to
			// block instead.
			if got := <-store.entered; got != "load acme" {
				t.Fatalf("first store call = %q", got)
			}
			if stuck == "save" {
				for _, want := range []string{"load acme", "save acme"} {
					store.release <- struct{}{}
					if got := <-store.entered; got != want {
						t.Fatalf("store call = %q, want %q", got, want)
					}
				}
			}

			other := make(chan error, 1)
			go func() { other <- l.Check(context.Background(), "globex", "", right, never) }()
			select {
			case err := <-other:
				if err != nil {
					t.Fatalf("globex check = %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("another key's check waited on acme's stuck %s", stuck)
			}
			close(store.release)
			<-done
		})
	}
}

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
	if got, err := s.LoadKnownGood("acme"); err != nil || got != nil {
		t.Fatalf("absent = %v, %v", got, err)
	}
	if err := s.SaveKnownGood("acme", []string{"id:acme/so"}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LoadKnownGood("acme"); err != nil || len(got) != 1 || got[0] != "id:acme/so" {
		t.Fatalf("reloaded = %v, %v", got, err)
	}
	var opErr *OperationError
	kv.data[soKnownGoodPrefix+"acme"] = []byte("{")
	if _, err := s.LoadKnownGood("acme"); !errors.As(err, &opErr) {
		t.Fatalf("corrupt = %v", err)
	}
	kv.getErr = errors.New("raft down")
	if _, err := s.LoadKnownGood("acme"); !errors.As(err, &opErr) {
		t.Fatalf("get failure = %v", err)
	}
	kv.putErr = errors.New("raft down")
	if err := s.SaveKnownGood("acme", nil); !errors.As(err, &opErr) {
		t.Fatalf("put failure = %v", err)
	}
}

// failingKnownGood is a SOPINKnownGoodStore that cannot read or write.
type failingKnownGood struct{}

func (failingKnownGood) LoadKnownGood(string) ([]string, error) {
	return nil, errors.New("store down")
}
func (failingKnownGood) SaveKnownGood(string, []string) error { return errors.New("store down") }

// TestSOPINLockout_KnownGoodStoreFaultsNeverRefuseAGoodPIN: a store that
// cannot load leaves the set to be loaded on a later check, and one that
// cannot save keeps the exemption in memory; neither turns a correct PIN into
// a refusal.
func TestSOPINLockout_KnownGoodStoreFaultsNeverRefuseAGoodPIN(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := NewSOPINLockout(func() time.Time { return now }, WithKnownGoodStore(failingKnownGood{}))
	right := func() error { return nil }
	never := func(error) bool { return false }
	if err := l.Check("node", "id:system/officer", right, never); err != nil {
		t.Fatalf("check with a failing store = %v", err)
	}
	state := l.keys["node"]
	if state == nil || !state.isKnownGood("id:system/officer") || state.knownGoodLoaded {
		t.Fatalf("state = %+v, want the exemption in memory and the load retried later", state)
	}
}

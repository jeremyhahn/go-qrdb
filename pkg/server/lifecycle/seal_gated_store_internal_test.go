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

package lifecycle

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// gateTestBarrier is an initialized, unsealed system barrier over memory.
func gateTestBarrier(t *testing.T, pass string) *seal.StorageBarrier {
	t.Helper()
	ctx := context.Background()
	strategy, err := seal.NewSoftwareStrategy(ctx, []byte(pass))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	b, err := seal.NewStorageBarrier(slog.Default(), seal.NewMemoryBackend(), seal.BarrierConfig{}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := b.Initialize(ctx, seal.MustCredentials(ctx, []byte(pass))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// sealGatedVerbs runs every credentials.Store verb of s once.
func sealGatedVerbs(ctx context.Context, s sealGatedStore) map[string]error {
	errs := map[string]error{}
	errs["Put"] = s.Put(ctx, "k", []byte("v1"))
	_, errs["Get"] = s.Get(ctx, "k")
	_, errs["List"] = s.List(ctx, "")
	_, errs["PutIfAbsent"] = s.PutIfAbsent(ctx, "fresh", []byte("v"))
	_, errs["CompareAndSwap"] = s.CompareAndSwap(ctx, "k", []byte("v1"), []byte("v2"))
	_, errs["PutAllIfAllUnchanged"] = s.PutAllIfAllUnchanged(ctx,
		[]kvstore.Guard{{Key: "k", Value: []byte("v2")}}, []kvstore.KV{{Key: "k", Value: []byte("v3")}})
	errs["Delete"] = s.Delete(ctx, "fresh")
	return errs
}

// TestSealGatedStore_ServesOnlyWhileTheSystemBarrierIsUnsealed: every verb
// reaches the data-plane store while the system barrier is unsealed, and
// every verb is refused with seal.ErrSealed once it is sealed.
func TestSealGatedStore_ServesOnlyWhileTheSystemBarrierIsUnsealed(t *testing.T) {
	ctx := context.Background()
	gate := gateTestBarrier(t, "system-pass")
	s := sealGatedStore{Store: gateTestBarrier(t, "data-pass"), barrier: gate}

	for verb, err := range sealGatedVerbs(ctx, s) {
		if err != nil {
			t.Errorf("%s while unsealed: %v", verb, err)
		}
	}
	if got, err := s.Get(ctx, "k"); err != nil || string(got) != "v3" {
		t.Fatalf("Get after the guarded batch = (%q, %v), want v3", got, err)
	}

	if err := gate.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	for verb, err := range sealGatedVerbs(ctx, s) {
		if !errors.Is(err, seal.ErrSealed) {
			t.Errorf("%s while sealed = %v, want seal.ErrSealed", verb, err)
		}
	}
}

// TestCredentialStoreOnDataPlane_OnlyEncryptingDurableBackends: the
// credential records go to the data plane only where the caller names an
// engine that survives a restart and passes an enabled at-rest Encryptor.
// A caller naming no backend (the desktop embedded binding) or passing no
// Encryptor, or a no-op one, keeps the barrier store.
func TestCredentialStoreOnDataPlane_OnlyEncryptingDurableBackends(t *testing.T) {
	enc := atrest.New(&seal.StorageBarrier{})
	cases := []struct {
		infra Infra
		want  bool
	}{
		{Infra{Backend: database.BackendPebble, Encryptor: enc}, true},
		{Infra{Backend: database.BackendFile, Encryptor: enc}, true},
		{Infra{Backend: database.BackendRaft, Encryptor: enc}, true},
		{Infra{Backend: database.BackendMemory, Encryptor: enc}, false},
		{Infra{Backend: database.BackendObjstore, Encryptor: enc}, false},
		{Infra{Backend: "", Encryptor: enc}, false},
		{Infra{Backend: database.BackendPebble}, false},
		{Infra{Backend: database.BackendRaft, Encryptor: atrest.New(nil)}, false},
		{Infra{}, false},
	}
	for _, c := range cases {
		if got := credentialStoreOnDataPlane(c.infra); got != c.want {
			t.Errorf("credentialStoreOnDataPlane(backend=%q, encryptor enabled=%v) = %v, want %v",
				c.infra.Backend, c.infra.Encryptor.Enabled(), got, c.want)
		}
	}
}

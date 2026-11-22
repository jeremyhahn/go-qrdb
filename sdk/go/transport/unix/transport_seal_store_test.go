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

package unix

import (
	"context"
	"errors"
	"slices"
	"sort"
	"sync"
	"testing"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// memSealStore is an in-memory service.SealStoreServicer: a named-secret map
// that answers a missing name the way the node's seal store does.
type memSealStore struct {
	mu      sync.Mutex
	secrets map[string][]byte
	reseals int
}

var _ service.SealStoreServicer = (*memSealStore)(nil)

func newMemSealStore() *memSealStore { return &memSealStore{secrets: map[string][]byte{}} }

func (m *memSealStore) missing(op, name string) error {
	return &seal.SealStoreError{Op: op, Key: name, Err: seal.ErrSealStoreSecretNotFound}
}

func (m *memSealStore) Put(_ context.Context, name string, secret []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.secrets[name] = append([]byte(nil), secret...)
	return nil
}

func (m *memSealStore) Get(_ context.Context, name string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.secrets[name]
	if !ok {
		return nil, m.missing("get", name)
	}
	return append([]byte(nil), s...), nil
}

func (m *memSealStore) Delete(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.secrets[name]; !ok {
		return m.missing("delete", name)
	}
	delete(m.secrets, name)
	return nil
}

func (m *memSealStore) names() []string {
	out := make([]string, 0, len(m.secrets))
	for n := range m.secrets {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (m *memSealStore) List(_ context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.names(), nil
}

func (m *memSealStore) Reseal(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.secrets[name]; !ok {
		return m.missing("reseal", name)
	}
	m.reseals++
	return nil
}

func (m *memSealStore) Status(_ context.Context) (*seal.SealStoreStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := m.names()
	return &seal.SealStoreStatus{Available: true, Strategy: seal.StrategyPassphrase, SecretCount: len(n), SecretNames: n}, nil
}

// connectSealStoreTransport serves store (nil: no seal store wired) over the
// unix socket and returns a connected Transport.
func connectSealStoreTransport(t *testing.T, store service.SealStoreServicer) *Transport {
	t.Helper()
	var opts []unixserver.ServerOption
	if store != nil {
		opts = append(opts, unixserver.WithSealStoreService(store))
	}
	tr, cleanup := startAuthServer(t, opts...)
	t.Cleanup(cleanup)
	return tr
}

func TestUnixSealStore_Calls(t *testing.T) {
	store := newMemSealStore()
	tr := connectSealStoreTransport(t, store)
	ctx := context.Background()

	for _, name := range []string{"alpha", "beta"} {
		if err := tr.SealStorePut(ctx, &transport.SealStorePutRequest{Name: name, Secret: []byte("s3cret-" + name)}); err != nil {
			t.Fatalf("SealStorePut(%s) = %v", name, err)
		}
	}
	got, err := tr.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "alpha"})
	if err != nil || string(got.Secret) != "s3cret-alpha" {
		t.Fatalf("SealStoreGet = %+v, %v", got, err)
	}
	list, err := tr.SealStoreList(ctx)
	if err != nil || !slices.Equal(list.Names, []string{"alpha", "beta"}) {
		t.Fatalf("SealStoreList = %+v, %v", list, err)
	}
	if err := tr.SealStoreReseal(ctx, &transport.SealStoreResealRequest{Name: "alpha"}); err != nil || store.reseals != 1 {
		t.Fatalf("SealStoreReseal = %v, reseals=%d", err, store.reseals)
	}
	st, err := tr.SealStoreStatus(ctx)
	if err != nil || !st.Available || st.SealerID != string(seal.StrategyPassphrase) || st.SecretCount != 2 || !slices.Equal(st.SecretNames, []string{"alpha", "beta"}) {
		t.Fatalf("SealStoreStatus = %+v, %v", st, err)
	}
	if err := tr.SealStoreDelete(ctx, &transport.SealStoreDeleteRequest{Name: "alpha"}); err != nil {
		t.Fatalf("SealStoreDelete = %v", err)
	}
	if _, ok := store.secrets["alpha"]; ok {
		t.Fatal("SealStoreDelete left the secret in the store")
	}
}

func TestUnixSealStore_ServerErrors(t *testing.T) {
	tr := connectSealStoreTransport(t, newMemSealStore())
	ctx := context.Background()
	var nf *transport.KeyNotFoundError
	if _, err := tr.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "absent"}); !errors.As(err, &nf) {
		t.Fatalf("SealStoreGet of a missing secret = %v, want KeyNotFoundError", err)
	}
	if err := tr.SealStoreDelete(ctx, &transport.SealStoreDeleteRequest{Name: "absent"}); err == nil {
		t.Fatal("SealStoreDelete of a missing secret succeeded")
	}
	if err := tr.SealStoreReseal(ctx, &transport.SealStoreResealRequest{Name: "absent"}); err == nil {
		t.Fatal("SealStoreReseal of a missing secret succeeded")
	}

	// A node with no seal store wired refuses every call.
	bare := connectSealStoreTransport(t, nil)
	var ce *transport.CapabilityUnsupportedError
	if err := bare.SealStorePut(ctx, &transport.SealStorePutRequest{Name: "a", Secret: []byte("x")}); err == nil {
		t.Fatal("SealStorePut on a node without a seal store succeeded")
	}
	if _, err := bare.SealStoreList(ctx); err == nil {
		t.Fatal("SealStoreList on a node without a seal store succeeded")
	}
	if _, err := bare.SealStoreStatus(ctx); !errors.As(err, &ce) {
		t.Fatalf("SealStoreStatus on a node without a seal store = %v, want CapabilityUnsupportedError", err)
	}
}

func TestUnixSealStore_Closed(t *testing.T) {
	tr := connectSealStoreTransport(t, newMemSealStore())
	_ = tr.Close()
	ctx := context.Background()
	var ce *transport.ClosedError
	if err := tr.SealStorePut(ctx, &transport.SealStorePutRequest{Name: "a", Secret: []byte("x")}); !errors.As(err, &ce) {
		t.Fatalf("SealStorePut = %T, want ClosedError", err)
	}
	if _, err := tr.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "a"}); !errors.As(err, &ce) {
		t.Fatalf("SealStoreGet = %T, want ClosedError", err)
	}
	if err := tr.SealStoreDelete(ctx, &transport.SealStoreDeleteRequest{Name: "a"}); !errors.As(err, &ce) {
		t.Fatalf("SealStoreDelete = %T, want ClosedError", err)
	}
	if _, err := tr.SealStoreList(ctx); !errors.As(err, &ce) {
		t.Fatalf("SealStoreList = %T, want ClosedError", err)
	}
	if err := tr.SealStoreReseal(ctx, &transport.SealStoreResealRequest{Name: "a"}); !errors.As(err, &ce) {
		t.Fatalf("SealStoreReseal = %T, want ClosedError", err)
	}
	if _, err := tr.SealStoreStatus(ctx); !errors.As(err, &ce) {
		t.Fatalf("SealStoreStatus = %T, want ClosedError", err)
	}
}

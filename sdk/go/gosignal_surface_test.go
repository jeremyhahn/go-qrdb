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

package sdk_test

// The go-signal surface tests compile and run against the sdk module alone:
// nothing here imports a package under go-qrdb/pkg, which is the point. Each
// test stands for one thing go-signal (docs/18 V38) used to reach into
// go-qrdb/pkg for.
//
// go-signal import          sdk/go equivalent
//   pkg/dao                 sdk.GenericDAO, sdk.NewDAO, sdk.DAOSortDescending
//   pkg/kvstore             sdk.KVStore, sdk.EntityIndex (RegisterEntityIndexes)
//   pkg/index               sdk.CompoundIndexDef, sdk.CompoundIndexRegisterer
//   pkg/storage             sdkstorage.Storage, sdkstorage.StorageIteratorOptions
//   pkg/storage/pebble      sdkstorage.NewPebbleStorage, sdkstorage.DefaultPebbleConfig
//   pkg/service             sdk.KVServicer (service-backed store: sdk/go/server,
//                           proven over a booted node in sdk/go/conformance
//                           TestServiceKVStore_* and TestDAOStoreParity)
//   pkg/engine              sdk.NewEmbeddedKVStore

import (
	"context"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	sdkstorage "github.com/jeremyhahn/go-qrdb/sdk/go/storage"
)

type signalSession struct {
	ID       uint64 `json:"id"`
	DeviceID string `json:"device_id" index:"true"`
	Peer     string `json:"peer"`
	Epoch    string `json:"epoch"`
}

func (s *signalSession) EntityID() uint64      { return s.ID }
func (s *signalSession) SetEntityID(id uint64) { s.ID = id }

func (s *signalSession) CompoundIndexes() []sdk.CompoundIndexDef {
	return []sdk.CompoundIndexDef{{Fields: []string{"Peer", "Epoch"}, Unique: true}}
}

var _ sdk.CompoundIndexRegisterer = (*signalSession)(nil)

func newSignalStore(t *testing.T) sdk.KVStore {
	t.Helper()
	stg, err := sdkstorage.NewPebbleStorage(sdkstorage.DefaultPebbleConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("NewPebbleStorage: %v", err)
	}
	t.Cleanup(func() { _ = stg.Close() })
	store, err := sdk.NewEmbeddedKVStore(stg)
	if err != nil {
		t.Fatalf("NewEmbeddedKVStore: %v", err)
	}
	return store
}

func TestGoSignalSurface_CompoundIndexEntity(t *testing.T) {
	defs := (&signalSession{}).CompoundIndexes()
	if len(defs) != 1 || len(defs[0].Fields) != 2 || !defs[0].Unique {
		t.Fatalf("CompoundIndexes = %+v", defs)
	}
}

func TestGoSignalSurface_EmbeddedKVStore(t *testing.T) {
	ctx := context.Background()
	store := newSignalStore(t)
	if err := store.Put(ctx, "k/1", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, err := store.Get(ctx, "k/1"); err != nil || string(got) != "v" {
		t.Fatalf("Get = %q, %v", got, err)
	}
}

func TestGoSignalSurface_DAO(t *testing.T) {
	ctx := context.Background()
	dao, err := sdk.NewDAO(newSignalStore(t), "session", func() *signalSession { return &signalSession{} })
	if err != nil {
		t.Fatalf("NewDAO: %v", err)
	}
	if err := dao.Save(ctx, &signalSession{ID: 7, DeviceID: "dev-a", Peer: "p", Epoch: "1"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	found, err := dao.Get(ctx, 7)
	if err != nil || found.DeviceID != "dev-a" {
		t.Fatalf("Get = %+v, %v", found, err)
	}
	hit, err := dao.QueryOneByIndex(ctx, "DeviceID", "dev-a")
	if err != nil || hit.ID != 7 {
		t.Fatalf("QueryOneByIndex = %+v, %v", hit, err)
	}
	if err := dao.Delete(ctx, found); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := dao.Get(ctx, 7); !sdk.IsDAONotFound(err) {
		t.Fatalf("Get after Delete = %v, want not found", err)
	}
}

// go-signal pkg/daostore/embedded.go and kvstore_adapter.go hold these types.
var (
	_ sdkstorage.Storage                = (sdkstorage.Storage)(nil)
	_ sdkstorage.StorageIteratorOptions = sdkstorage.StorageIteratorOptions{}
	_ sdk.EntityIndex                   = sdk.EntityIndex{}
	_ sdk.KVServicer                    = (sdk.KVServicer)(nil)
)

// signalSessionStore mirrors go-signal pkg/daostore/factory.go and
// pkg/desktop/jobs/queue.go, which keep a GenericDAO as a struct field.
type signalSessionStore struct {
	sessions sdk.GenericDAO[*signalSession]
}

func newSignalSessionStore(t *testing.T) *signalSessionStore {
	t.Helper()
	d, err := sdk.NewDAO(newSignalStore(t), "session", func() *signalSession { return &signalSession{} })
	if err != nil {
		t.Fatalf("NewDAO: %v", err)
	}
	return &signalSessionStore{sessions: d}
}

func TestGoSignalSurface_FindOrderByDescending(t *testing.T) {
	ctx := context.Background()
	holder := newSignalSessionStore(t)
	for i, peer := range []string{"bob", "carol", "alice"} {
		s := &signalSession{ID: uint64(i + 1), DeviceID: "dev", Peer: peer, Epoch: "1"}
		if err := holder.sessions.Save(ctx, s); err != nil {
			t.Fatalf("Save %s: %v", peer, err)
		}
	}
	got, err := holder.sessions.Find(ctx).OrderBy("Peer", sdk.DAOSortDescending).Execute()
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := []string{"carol", "bob", "alice"}
	if len(got) != len(want) {
		t.Fatalf("Execute returned %d rows, want %d", len(got), len(want))
	}
	for i, s := range got {
		if s.Peer != want[i] {
			t.Fatalf("row %d Peer = %q, want %q", i, s.Peer, want[i])
		}
	}
}

func TestGoSignalSurface_IndexRequestTypes(t *testing.T) {
	requests := []any{
		sdk.IndexQueryRequest{IndexName: "session.DeviceID", Value: []byte("dev-a")},
		sdk.IndexScanRequest{IndexName: "session.DeviceID", Start: []byte("a"), End: []byte("z")},
		sdk.CompoundIndexQueryRequest{IndexName: "session.Peer_Epoch", Values: [][]byte{[]byte("p"), []byte("1")}},
		sdk.CompoundIndexScanRequest{IndexName: "session.Peer_Epoch", PrefixValues: [][]byte{[]byte("p")}},
	}
	if len(requests) != 4 {
		t.Fatalf("constructed %d requests", len(requests))
	}
}

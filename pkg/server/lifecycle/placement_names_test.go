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
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
)

// stubCatalog serves ListTenant from a fixed table; a tenant it does not
// hold fails as a sealed tenant's catalog does.
type stubCatalog struct {
	database.Catalog
	metas map[string][]database.DatabaseMeta
}

func (c stubCatalog) ListTenant(_ context.Context, tenantID string) ([]database.DatabaseMeta, error) {
	metas, ok := c.metas[tenantID]
	if !ok {
		return nil, errors.New("tenant catalog unreadable")
	}
	return metas, nil
}

// The resolver names a tenant's databases from its catalog, always names its
// home group, and resolves nothing else when the catalog is unreadable.
func TestCatalogNames_ResolvesFromTheTenantsCatalog(t *testing.T) {
	names := catalogNames{catalog: stubCatalog{metas: map[string][]database.DatabaseMeta{
		"acme": {{ID: "id-1", Name: "orders"}, {Name: "no-id"}},
	}}}
	got := names.DatabaseNames(context.Background(), "acme")
	if got["id-1"] != "orders" || got[database.TenantHomeID] != database.TenantHomeName || len(got) != 2 {
		t.Fatalf("acme names = %v", got)
	}
	sealed := names.DatabaseNames(context.Background(), "sealed")
	if len(sealed) != 1 || sealed[database.TenantHomeID] != database.TenantHomeName {
		t.Fatalf("sealed tenant names = %v; want only the home group", sealed)
	}
}

// setPlacementNames sets the resolver on the store and on its change feed.
func TestSetPlacementNames_SetsStoreAndFeed(t *testing.T) {
	setPlacementNames(&Node{}, nil)
	feed, hub := newPlacementStoreOverHub(t)
	store, _ := newPlacementStoreOverHub(t)
	setPlacementNames(&Node{PlacementStore: store, PlacementChanges: feed}, stubCatalog{metas: map[string][]database.DatabaseMeta{
		"acme": {{ID: "id-1", Name: "orders"}},
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := feed.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(placement.Placement{TenantID: "acme", DatabaseID: "id-1", ShardID: 3})
	key := []byte(placement.PlacementPrefix + "acme/id-1")
	hub.NotifyPut(key, &kv.KeyValue{Key: key, Value: raw}, nil)
	if ev := nextPlacementEvent(t, events); ev.Database != "orders" {
		t.Fatalf("feed event = %+v; want the name from the catalog", ev)
	}
	if err := store.Put(ctx, &placement.Placement{TenantID: "acme", DatabaseID: "id-1", ShardID: 3}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Get(ctx, "acme", "id-1"); err != nil || got.Database != "orders" {
		t.Fatalf("store Get = %+v, %v; want the name from the catalog", got, err)
	}
}

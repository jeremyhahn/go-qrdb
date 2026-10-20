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
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// memberRecords is a KV double holding members' records under
// cluster.MembershipPrefix, and one record that does not decode.
func memberRecords(t *testing.T, members ...cluster.Member) *mockKVServicer {
	t.Helper()
	records := map[string][]byte{cluster.MembershipPrefix + "garbage": []byte("{")}
	for _, m := range members {
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		records[cluster.MembershipPrefix+m.Address] = raw
	}
	return &mockKVServicer{scanFn: func(_ context.Context, prefix string) (map[string][]byte, error) {
		if prefix != cluster.MembershipPrefix {
			t.Fatalf("scanned %q, want the member records", prefix)
		}
		return records, nil
	}}
}

// placementTable is a placement table holding ps.
func placementTable(t *testing.T, ps ...*placement.Placement) *placement.Store {
	t.Helper()
	store, _ := newPlacementStoreOverHub(t)
	for _, p := range ps {
		if err := store.Put(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// scanFailingKV is a placement.KV whose every read fails.
type scanFailingKV struct{ placement.KV }

func (scanFailingKV) Scan(context.Context, string, func(string, []byte) error) error {
	return errors.New("placement store down")
}

func TestBarrierNodes_ListsEveryNodeHoldingAReplicaWithItsEndpoints(t *testing.T) {
	placements := placementTable(t,
		&placement.Placement{TenantID: "acme", Database: "orders", ShardID: 1001, RF: 2, LeaderReplicaID: 1,
			Replicas: []placement.Replica{{ReplicaID: 1, NodeID: "n2:1", RaftAddr: "n2:1"}, {ReplicaID: 2, NodeID: "n1:1", RaftAddr: "n1:1"}}},
		&placement.Placement{TenantID: "acme", Database: "users", ShardID: 1002, RF: 2, LeaderReplicaID: 1,
			Replicas: []placement.Replica{{ReplicaID: 1, NodeID: "n1:1", RaftAddr: "n1:1"}, {ReplicaID: 2, NodeID: "n3:1", RaftAddr: "n3:1"}}},
		&placement.Placement{TenantID: "other", Database: "orders", ShardID: 2001, RF: 1, LeaderReplicaID: 1,
			Replicas: []placement.Replica{{ReplicaID: 1, NodeID: "n5:1", RaftAddr: "n5:1"}}},
	)
	members := memberRecords(t,
		cluster.Member{NodeID: 1, Address: "n1:1", APIEndpoints: map[cluster.APIProtocol]string{
			cluster.APIProtocolREST: "n1:2", cluster.APIProtocolGRPC: "n1:3"}},
		cluster.Member{NodeID: 2, Address: "n2:1", APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolQUIC: "n2:4"}},
	)

	got, err := barrierNodes{placements: placements, members: members}.BarrierNodes(context.Background(), "acme")
	if err != nil {
		t.Fatalf("BarrierNodes: %v", err)
	}
	want := []tenant.BarrierNode{
		{Node: "n1:1", Endpoints: map[string]string{"rest": "n1:2", "grpc": "n1:3"}},
		{Node: "n2:1", Endpoints: map[string]string{"quic": "n2:4"}},
		{Node: "n3:1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BarrierNodes = %+v, want %+v", got, want)
	}
}

func TestBarrierNodes_NoGroupsReadsNoMembers(t *testing.T) {
	members := &mockKVServicer{scanFn: func(context.Context, string) (map[string][]byte, error) {
		t.Fatal("read the member records for a tenant with no groups")
		return nil, nil
	}}
	got, err := barrierNodes{placements: placementTable(t), members: members}.BarrierNodes(context.Background(), "acme")
	if err != nil || got != nil {
		t.Fatalf("BarrierNodes = %v, %v; want none", got, err)
	}
}

func TestBarrierNodes_ReadErrors(t *testing.T) {
	failing, err := placement.NewStore(scanFailingKV{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (barrierNodes{placements: failing, members: memberRecords(t)}).BarrierNodes(context.Background(), "acme"); err == nil {
		t.Fatal("BarrierNodes listed nodes from an unreadable placement table")
	}

	placements := placementTable(t, &placement.Placement{TenantID: "acme", Database: "orders", ShardID: 1001, RF: 1, LeaderReplicaID: 1,
		Replicas: []placement.Replica{{ReplicaID: 1, NodeID: "n1:1", RaftAddr: "n1:1"}}})
	down := errors.New("members unreadable")
	members := &mockKVServicer{scanFn: func(context.Context, string) (map[string][]byte, error) { return nil, down }}
	if _, err := (barrierNodes{placements: placements, members: members}).BarrierNodes(context.Background(), "acme"); !errors.Is(err, down) {
		t.Fatalf("BarrierNodes = %v, want the member read's error", err)
	}
}

// nodeSourceWiring records the source a composition wires.
type nodeSourceWiring struct {
	tenant.TenantServicer
	source tenant.BarrierNodeSource
}

func (w *nodeSourceWiring) SetBarrierNodeSource(s tenant.BarrierNodeSource) { w.source = s }

func TestWireBarrierNodes(t *testing.T) {
	placements := placementTable(t)
	wiring := &nodeSourceWiring{}
	wireBarrierNodes(&Node{Tenant: wiring, KV: &mockKVServicer{}})
	if wiring.source != nil {
		t.Fatal("wired a node source without a placement table")
	}
	wireBarrierNodes(&Node{Tenant: wiring, PlacementStore: placements})
	if wiring.source != nil {
		t.Fatal("wired a node source without the member records")
	}
	wireBarrierNodes(&Node{Tenant: wiring, PlacementStore: placements, KV: &mockKVServicer{}})
	if _, ok := wiring.source.(barrierNodes); !ok {
		t.Fatalf("wired %T, want barrierNodes", wiring.source)
	}
}

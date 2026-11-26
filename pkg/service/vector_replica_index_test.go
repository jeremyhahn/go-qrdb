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

package service_test

import (
	"context"
	"testing"

	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"

	raftbackend "github.com/jeremyhahn/go-qrdb/pkg/backend/raft"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/store"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/raftharness"
)

// replicaVectorHost is a raft host adapter whose group runs this binary on
// every replica, so every feature gate is open.
type replicaVectorHost struct {
	*raftbackend.HostAdapter
}

func (replicaVectorHost) FeatureEnabled(_ uint64, _ feature.Feature) bool { return true }

func searchIDs(t *testing.T, svc service.VectorService, query []float32) map[uint64]bool {
	t.Helper()
	resp, err := svc.SearchSimilar(context.Background(), &service.SearchSimilarRequest{Namespace: "ns", QueryVector: query, K: 10})
	if err != nil {
		t.Fatalf("SearchSimilar: %v", err)
	}
	ids := make(map[uint64]bool, len(resp.Results))
	for _, r := range resp.Results {
		ids[r.ID] = true
	}
	return ids
}

// TestVectorService_ReplicaSearchSeesReplicatedApplies: a search on
// replica B, warmed before the write, sees a vector written and then a
// vector deleted through replica A. Each replica's engine-owned index is
// updated by the replicated apply itself, not by a per-service cache that
// an apply on another node never reaches.
func TestVectorService_ReplicaSearchSeesReplicatedApplies(t *testing.T) {
	const shardID = 4242
	c := raftharness.NewCluster(t, 2)
	members := map[uint64]string{1: c.Addrs[0], 2: c.Addrs[1]}
	for i, host := range c.Hosts {
		if err := host.StartShard(members, false, store.NewMVCCCreateFunc(store.WithMemFS()), quicraftconfig.Config{
			ShardID: shardID, ReplicaID: uint64(i + 1), MemberIdentities: c.Identities,
		}); err != nil {
			t.Fatalf("StartShard(host %d): %v", i, err)
		}
	}
	c.WaitReadable(t, shardID, c.Hosts...)

	newSvc := func(i int) service.VectorService {
		svc, err := service.NewVectorService(nil, hostVecLogger(t),
			service.WithVectorApplyHost(replicaVectorHost{raftbackend.New(c.Hosts[i])}, shardID))
		if err != nil {
			t.Fatalf("NewVectorService(host %d): %v", i, err)
		}
		t.Cleanup(func() { closeVectorService(svc) })
		return svc
	}
	a, b := newSvc(0), newSvc(1)
	ctx := context.Background()

	if _, err := a.CreateNamespace(ctx, &service.CreateNamespaceRequest{Namespace: "ns", Dimension: 2, DistanceMetric: "COSINE"}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	if _, err := a.UpsertVector(ctx, &service.UpsertVectorRequest{Namespace: "ns", ID: 1, Vector: []float32{1, 0}}); err != nil {
		t.Fatalf("UpsertVector(1): %v", err)
	}
	// B's search is a linearizable read of B's own replica: it sees every
	// write A's proposal returned for, without waiting.
	waitIDs := func(want map[uint64]bool) {
		t.Helper()
		got := searchIDs(t, b, []float32{1, 0})
		match := len(got) == len(want)
		for id := range want {
			match = match && got[id]
		}
		if !match {
			t.Fatalf("replica B search = %v, want %v", got, want)
		}
	}
	// Warm B's search before the writes under test.
	waitIDs(map[uint64]bool{1: true})

	if _, err := a.UpsertVector(ctx, &service.UpsertVectorRequest{Namespace: "ns", ID: 2, Vector: []float32{0.9, 0.1}}); err != nil {
		t.Fatalf("UpsertVector(2): %v", err)
	}
	if _, err := a.DeleteVector(ctx, &service.DeleteVectorRequest{Namespace: "ns", ID: 1}); err != nil {
		t.Fatalf("DeleteVector(1): %v", err)
	}
	waitIDs(map[uint64]bool{2: true})
}

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

//go:build !integration

package node

import (
	"bytes"
	"context"
	"strconv"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
)

// TestClusteredNodes_FreshClusterServesThroughClusterSealActivation: a fresh
// three-node cluster with the seal on (strategy none) and no seal record
// serves every request from the moment it has a leader -- before cluster-seal
// activates, as it activates, and after -- with no retry. Every node is asked
// on every round until all of them report cluster-seal active and serving,
// so a node that refused while it learned the activations, or while it
// waited for its first read of the records after activation, fails the test.
func TestClusteredNodes_FreshClusterServesThroughClusterSealActivation(t *testing.T) {
	ca, replicas := newFormationReplicas(t, 3)
	sealed := func(cfg *qrdbconfig.Config) {
		cfg.Seal.Enabled = true
		cfg.Seal.Strategy = "none"
	}
	nodes, err := bootFormationClusterWith(t, ca, replicas, func(self uint64) string {
		return peerIdentitySpecs(t, self, replicas)
	}, sealed)
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), clusterFormationBudget)
	defer cancel()

	leader := nodes[awaitAgreedLeader(t, nodes)-1]
	for round := 0; ; round++ {
		key := "activation/" + strconv.Itoa(round)
		value := []byte(strconv.Itoa(round))
		if err := leader.Client().KV().Put(ctx, key, value); err != nil {
			t.Fatalf("round %d: Put through the leader: %v", round, err)
		}
		serving := 0
		for i, n := range nodes {
			got, err := n.Client().KV().Get(ctx, key)
			if err != nil || !bytes.Equal(got, value) {
				t.Fatalf("round %d: node %d read %q, %v", round, replicas[i].id, got, err)
			}
			st, err := n.Client().Barrier().BarrierStatus(ctx)
			if err != nil {
				t.Fatalf("round %d: node %d BarrierStatus: %v", round, replicas[i].id, err)
			}
			if cs := st.ClusterSeal; cs != nil && cs.Active && cs.Local == "serving" {
				serving++
			}
		}
		if serving == len(nodes) {
			return
		}
		if ctx.Err() != nil {
			t.Fatalf("cluster-seal never active and serving on every node after %d rounds", round)
		}
	}
}

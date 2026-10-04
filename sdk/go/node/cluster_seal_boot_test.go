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
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestClusteredNode_FirstRequestAfterRestartUnderASealedRecordIsRefused pins
// "read the record before serving on restart" for the SDK node, whose
// feature gate starts in the background: a cluster restarted under an
// activated cluster-seal feature and a sealed node record refuses each node's
// very first request after unseal -- it neither serves on a gate that has not
// read the activations nor on a monitor that has not read the records -- then
// settles held, and serves once one cluster-unseal clears the record.
func TestClusteredNode_FirstRequestAfterRestartUnderASealedRecordIsRefused(t *testing.T) {
	ca, replicas := newFormationReplicas(t, 3)
	caPEM := mustReadCAPEM(t, ca)
	members := make(map[uint64]string, len(replicas))
	for _, r := range replicas {
		members[r.id] = r.addr
	}
	cfgs := make([]*qrdbconfig.Config, len(replicas))
	mtls := make([]*RaftMTLSConfig, len(replicas))
	for i, r := range replicas {
		cfgs[i] = sealClustered(formationConfig(t, r, peerIdentitySpecs(t, r.id, replicas)))
		mtls[i] = &RaftMTLSConfig{CACert: caPEM, Cert: r.cert.CertPEM, Key: r.cert.KeyPEM}
	}
	// boot builds every node and unseals them together: each boot waits for
	// the system shard's leader, which needs a quorum of the others.
	boot := func() []*Node {
		nodes := make([]*Node, len(replicas))
		for i := range replicas {
			n, err := NewClusteredNode(context.Background(), cfgs[i], members, nil, WithRaftMTLS(mtls[i]))
			if err != nil {
				t.Fatalf("NewClusteredNode(%d): %v", replicas[i].id, err)
			}
			nodes[i] = n
			t.Cleanup(func() { _ = n.Close() })
		}
		errs := make([]error, len(nodes))
		var wg sync.WaitGroup
		for i, n := range nodes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = n.Unseal(context.Background(), seal.MustCredentials(context.Background(), []byte("wal-encryption-test")))
			}()
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			t.Fatalf("Unseal: %v", err)
		}
		return nodes
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*clusterFormationBudget)
	defer cancel()
	waitClusterSeal := func(n *Node, what string, ok func(*transport.ClusterSeal) bool) {
		t.Helper()
		for {
			st, err := n.Client().Barrier().BarrierStatus(ctx)
			if err == nil && st.ClusterSeal != nil && ok(st.ClusterSeal) {
				return
			}
			select {
			case <-ctx.Done():
				var cs any
				if st != nil {
					cs = st.ClusterSeal
				}
				t.Fatalf("timed out waiting for %s (last cluster seal: %+v, err %v)", what, cs, err)
			case <-time.After(clusterPollInterval):
			}
		}
	}

	nodes := boot()
	for _, n := range nodes {
		waitClusterSeal(n, "cluster-seal active and serving", func(cs *transport.ClusterSeal) bool {
			return cs.Active && cs.Local == "serving"
		})
	}
	if err := nodes[0].Client().Barrier().BarrierSeal(ctx); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}
	for _, n := range nodes {
		_ = n.Close()
	}

	restarted := boot()
	for i, n := range restarted {
		if _, err := n.Client().KV().Get(ctx, "any"); !errors.Is(err, seal.ErrSealed) {
			t.Fatalf("node %d's first request after restart under a sealed node record: err = %v, want the sealed refusal", replicas[i].id, err)
		}
	}
	for _, n := range restarted {
		waitClusterSeal(n, "held", func(cs *transport.ClusterSeal) bool { return cs.Local == "held" })
	}
	if _, err := restarted[0].Client().Barrier().BarrierClusterUnseal(ctx); err != nil {
		t.Fatalf("BarrierClusterUnseal: %v", err)
	}
	// Each node serves again once its monitor reads the cleared record.
	for _, n := range restarted {
		waitClusterSeal(n, "serving after the cluster unseal", func(cs *transport.ClusterSeal) bool { return cs.Local == "serving" })
	}
	if err := putUntilCommitted(ctx, restarted[0].Client().KV(), "after", []byte("v")); err != nil {
		t.Fatalf("Put after cluster-unseal: %v", err)
	}
}

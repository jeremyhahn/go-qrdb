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
	"slices"
	"sync"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestClusteredNode_FirstRequestAfterRestartUnderASealedRecordIsRefused pins
// "read the record before serving on restart" for the SDK node, whose
// feature gate starts in the background: a cluster restarted under an
// activated cluster-seal feature and a sealed node record refuses each node's
// very first request after unseal -- it neither serves on a gate that has not
// read the activations nor on a monitor that has not read the records, which
// refuses as SEAL_STATE_UNCONFIRMED -- then settles held, refusing as sealed,
// and serves once one cluster-unseal clears the record.
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
	// A seal or clear whose record the system shard could not confirm -- its
	// read or write met a leader election, which a loaded host can start at
	// any point here -- is the documented retryable refusal
	// (docs/barrier-seal/api.md). The node that refused a seal has sealed
	// itself anyway and refuses requests, so the seal is retried through the
	// next node, once the unsealed nodes have a leader among themselves and a
	// write through that node commits: retrying straight into the election,
	// or toward a sealed leader, would seal that node too, and two sealed
	// nodes leave the third no quorum. The refused seal may have committed after all, in
	// which case every node seals on reading it and nothing is retried.
	for sealer := 0; ; {
		err := nodes[sealer].Client().Barrier().BarrierSeal(ctx)
		if err == nil {
			break
		}
		if !recordUnconfirmed(err) || sealer == len(nodes)-1 {
			t.Fatalf("BarrierSeal through node %d: %v", replicas[sealer].id, err)
		}
		sealer++
		if sealRecordedOrSettled(ctx, t, nodes[sealer:], replicas[sealer:]) {
			break
		}
	}
	for _, n := range nodes {
		_ = n.Close()
	}

	restarted := boot()
	// Boot starts the monitor without awaiting its first read, so the first
	// request meets a node that has either read the sealed record -- the
	// sealed refusal -- or not yet -- SEAL_STATE_UNCONFIRMED
	// (docs/barrier-seal/api.md). Either refuses; neither serves.
	for i, n := range restarted {
		_, err := n.Client().KV().Get(ctx, "any")
		if !errors.Is(err, seal.ErrSealed) && !sealStateUnconfirmed(err) {
			t.Fatalf("node %d's first request after restart under a sealed node record: err = %v, want the sealed or the seal-state-unconfirmed refusal", replicas[i].id, err)
		}
	}
	// Once a node has read the record it is held, which is sealed and never
	// unconfirmed; until then it refuses as SEAL_STATE_UNCONFIRMED. A held
	// node answers nothing but the unseal call and the cluster unseal: its
	// status is the same sealed refusal, which is all "held" looks like from
	// the node itself.
	for i, n := range restarted {
		for {
			_, err := n.Client().KV().Get(ctx, "any")
			if errors.Is(err, seal.ErrSealed) && !sealStateUnconfirmed(err) {
				break
			}
			if !sealStateUnconfirmed(err) {
				t.Fatalf("node %d under a sealed node record: err = %v, want the sealed or the seal-state-unconfirmed refusal", replicas[i].id, err)
			}
			select {
			case <-ctx.Done():
				t.Fatalf("node %d never read the sealed node record: last err = %v", replicas[i].id, err)
			case <-time.After(clusterPollInterval):
			}
		}
		if _, err := n.Client().Barrier().BarrierStatus(ctx); !transport.IsBarrierSealed(err) {
			t.Fatalf("node %d's barrier status while held: err = %v, want the sealed refusal", replicas[i].id, err)
		}
	}
	// A clear unseals nothing and a refused one changes nothing, so it is
	// retried through the same node.
	for {
		_, err := restarted[0].Client().Barrier().BarrierClusterUnseal(ctx)
		if err == nil {
			break
		}
		if !recordUnconfirmed(err) {
			t.Fatalf("BarrierClusterUnseal: %v", err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("BarrierClusterUnseal: %v", errors.Join(ctx.Err(), err))
		case <-time.After(clusterPollInterval):
		}
	}
	// Each node serves again once its monitor reads the cleared record.
	for _, n := range restarted {
		waitClusterSeal(n, "serving after the cluster unseal", func(cs *transport.ClusterSeal) bool { return cs.Local == "serving" })
	}
	if err := putUntilCommitted(ctx, restarted[0].Client().KV(), "after", []byte("v")); err != nil {
		t.Fatalf("Put after cluster-unseal: %v", err)
	}
}

// recordUnconfirmed reports whether err is a cluster seal record the system
// shard could not confirm, which docs/barrier-seal/api.md makes a retryable
// 503.
func recordUnconfirmed(err error) bool {
	var unconfirmed *clusterseal.RecordNotConfirmedError
	return errors.As(err, &unconfirmed)
}

// sealStateUnconfirmed reports whether err is the refusal of a node that
// cannot confirm the cluster seal state, which docs/barrier-seal/api.md makes
// a retryable 503 distinct from the sealed refusal.
func sealStateUnconfirmed(err error) bool {
	var unconfirmed *clusterseal.SealStateUnconfirmedError
	return errors.As(err, &unconfirmed)
}

// sealRecordedOrSettled waits, after a seal whose record was not confirmed,
// until nodes -- the ones the refused seal left unsealed -- report the node
// record sealed, the refused write having committed after all, and returns
// true; or until they agree on a system shard leader among themselves and a
// write through nodes[0] commits, so a seal through nodes[0] meets a settled
// leader rather than the node that sealed itself, and returns false.
// replicas[i] is nodes[i]'s replica.
func sealRecordedOrSettled(ctx context.Context, t *testing.T, nodes []*Node, replicas []formationReplica) bool {
	t.Helper()
	for {
		// A sealed node answers no status, so the record reads as this
		// node's barrier having sealed itself on reading it.
		if nodes[0].IsSealed() {
			return true
		}
		var err error
		if leaderAmong(t, nodes, replicas) {
			err = nodes[0].Client().KV().Put(ctx, "seal-retry-probe", []byte("v"))
			if err == nil {
				return false
			}
			var notLeader *transport.NotLeaderError
			if !errors.As(err, &notLeader) && !errors.Is(err, quicraft.ErrReadIndexNotReady) && !errors.Is(err, seal.ErrSealed) {
				t.Fatalf("probe write before retrying the seal: %v", err)
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("the node record neither sealed nor became writable: %v", errors.Join(ctx.Err(), err))
		case <-time.After(clusterPollInterval):
		}
	}
}

// leaderAmong reports whether every one of nodes reports the same system
// shard leader and that leader is one of them. replicas[i] is nodes[i]'s
// replica.
func leaderAmong(t *testing.T, nodes []*Node, replicas []formationReplica) bool {
	t.Helper()
	var leader uint64
	for _, n := range nodes {
		host, ok := n.host.(*quicraft.Host)
		if !ok {
			t.Fatalf("node host is %T, want *quicraft.Host", n.host)
		}
		id, known, err := host.LeaderID(shard.ShardKV)
		if err != nil || !known || (leader != 0 && id != leader) {
			return false
		}
		leader = id
	}
	return slices.ContainsFunc(replicas, func(r formationReplica) bool { return r.id == leader })
}

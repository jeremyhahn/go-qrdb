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
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestClusteredNode_EmbeddedWatchEndsOnClusterSeal pins the composition: the
// node hands its embedded transport the cluster-seal stream registry, so a
// watch the caller opened through the in-process client before a cluster
// seal is ended by that seal -- with a terminal frame -- on every node, not
// only refused when the next one opens.
func TestClusteredNode_EmbeddedWatchEndsOnClusterSeal(t *testing.T) {
	ca, replicas := newFormationReplicas(t, 3)
	caPEM := mustReadCAPEM(t, ca)
	members := make(map[uint64]string, len(replicas))
	for _, r := range replicas {
		members[r.id] = r.addr
	}
	nodes := make([]*Node, len(replicas))
	for i, r := range replicas {
		cfg := sealClustered(formationConfig(t, r, peerIdentitySpecs(t, r.id, replicas)))
		mtls := &RaftMTLSConfig{CACert: caPEM, Cert: r.cert.CertPEM, Key: r.cert.KeyPEM}
		n, err := NewClusteredNode(context.Background(), cfg, members, nil, WithRaftMTLS(mtls))
		if err != nil {
			t.Fatalf("NewClusteredNode(%d): %v", r.id, err)
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*clusterFormationBudget)
	defer cancel()
	for _, n := range nodes {
		for {
			st, err := n.Client().Barrier().BarrierStatus(ctx)
			if err == nil && st.ClusterSeal != nil && st.ClusterSeal.Active && st.ClusterSeal.Local == "serving" {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatalf("cluster seal never active and serving: %v", err)
			case <-time.After(clusterPollInterval):
			}
		}
	}

	watches := make([]<-chan transport.WatchResponse, len(nodes))
	for i, n := range nodes {
		ch, err := n.Client().Watch().Watch(ctx, "watched", transport.WatchOptions{})
		if err != nil {
			t.Fatalf("node %d Watch: %v", replicas[i].id, err)
		}
		watches[i] = ch
	}
	if err := nodes[0].Client().Barrier().BarrierSeal(ctx); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}
	for i, ch := range watches {
		var terminal bool
	drain:
		for {
			select {
			case resp, ok := <-ch:
				if !ok {
					break drain
				}
				if resp.Canceled {
					terminal = true
				}
			case <-ctx.Done():
				t.Fatalf("node %d's embedded watch was still open after the cluster seal", replicas[i].id)
			}
		}
		if !terminal {
			t.Fatalf("node %d's embedded watch closed with no terminal frame; a seal ends it with its refusal", replicas[i].id)
		}
	}
}

// TestClusteredNode_CloseWaitsForTheClusterSealMonitor proves the cluster seal
// monitor is node work Close waits for: it reads the seal records through the
// service graph and seals through the barriers Close tears down, so none the
// node started may still be running when Close returns. A Close that only
// cancels the monitor's ctx leaves the goroutine to exit on its own, which it
// usually does within microseconds, so this catches that defect only when the
// goroutine is still unwinding as Close returns; with the join it cannot fail.
func TestClusteredNode_CloseWaitsForTheClusterSealMonitor(t *testing.T) {
	before := clusterSealMonitors()
	ca, replicas := newFormationReplicas(t, 1)
	r := replicas[0]
	cfg := sealClustered(formationConfig(t, r, peerIdentitySpecs(t, r.id, replicas)))
	mtls := &RaftMTLSConfig{CACert: mustReadCAPEM(t, ca), Cert: r.cert.CertPEM, Key: r.cert.KeyPEM}
	n, err := NewClusteredNode(context.Background(), cfg, map[uint64]string{r.id: r.addr}, nil, WithRaftMTLS(mtls))
	if err != nil {
		t.Fatalf("NewClusteredNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	if err := n.Unseal(context.Background(), seal.MustCredentials(context.Background(), []byte("wal-encryption-test"))); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	// The monitor's goroutine may not have been scheduled yet when Unseal
	// returns; yield until it has.
	for clusterSealMonitors() <= before {
		runtime.Gosched()
	}

	if err := n.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := clusterSealMonitors(); got != before {
		t.Fatalf("cluster seal monitors running when Close returned = %d, want the %d before the node", got, before)
	}
}

// clusterSealMonitors counts the goroutines inside a cluster seal monitor's
// Run.
func clusterSealMonitors() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Count(string(buf[:n]), "clusterseal.(*Monitor).Run(")
		}
		buf = make([]byte, 2*len(buf))
	}
}

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
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// addBothTenantKinds brings up, on n's barrier registry, one system-managed
// (auto-unseal) tenant barrier and one self-managed tenant barrier, both
// initialized and unsealed: the keys a node holds beyond its own.
func addBothTenantKinds(t *testing.T, n *Node) {
	t.Helper()
	ctx := context.Background()
	reg := n.barrierRegistry.Load()
	if reg == nil {
		t.Fatal("the node has no barrier registry")
	}
	if _, err := reg.RegisterTenant(ctx, "auto", seal.NewMemoryBackend(), seal.BarrierConfig{RootKeyPath: "tenants/auto/root"}, seal.NewSystemBarrierStrategy(reg.SystemBarrier())); err != nil {
		t.Fatalf("register auto: %v", err)
	}
	if err := reg.InitializeTenant(ctx, "auto", seal.Credentials{}); err != nil {
		t.Fatalf("initialize auto: %v", err)
	}
	if _, err := reg.RegisterTenantDefault("self"); err != nil {
		t.Fatalf("register self: %v", err)
	}
	if err := reg.InitializeTenant(ctx, "self", seal.MustCredentials(context.Background(), []byte("self-pass"))); err != nil {
		t.Fatalf("initialize self: %v", err)
	}
}

// requireWholeNodeSealed fails unless n's node barrier and every tenant
// barrier on it are sealed with their keys wiped.
func requireWholeNodeSealed(t *testing.T, n *Node, who string) {
	t.Helper()
	reg := n.barrierRegistry.Load()
	if !reg.SystemBarrier().IsSealed() {
		t.Fatalf("%s: the node barrier is unsealed", who)
	}
	for _, id := range []string{"auto", "self"} {
		tb, err := reg.GetTenant(id)
		if err != nil {
			t.Fatalf("%s: GetTenant(%s): %v", who, id, err)
		}
		if !tb.IsSealed() || !tb.Barrier().EpochBarrier().IsSealed() {
			t.Fatalf("%s: tenant %s still holds its keys after the node was sealed", who, id)
		}
	}
}

// TestStandaloneNode_SystemSealSealsTheWholeNode: on a single node, sealing
// the node barrier through the client seals every tenant barrier with it, and
// unsealing it brings the system-managed tenant back while the self-managed
// one stays sealed for its own admin.
func TestStandaloneNode_SystemSealSealsTheWholeNode(t *testing.T) {
	const passphrase = "whole-node-passphrase"
	ctx := context.Background()
	cfg := sealedStandaloneConfig(t, t.TempDir())
	n, err := NewStandaloneNode(ctx, cfg, nil, WithSealPassphrase([]byte(passphrase)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	addBothTenantKinds(t, n)

	if err := n.Client().Barrier().BarrierSeal(ctx); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}
	requireWholeNodeSealed(t, n, "single node")

	if err := n.Client().Barrier().BarrierUnseal(ctx, &transport.BarrierUnsealRequest{Secret: []byte(passphrase)}); err != nil {
		t.Fatalf("BarrierUnseal: %v", err)
	}
	reg := n.barrierRegistry.Load()
	auto, _ := reg.GetTenant("auto")
	self, _ := reg.GetTenant("self")
	if auto.IsSealed() {
		t.Fatal("the system-managed tenant stayed sealed after the node unseal")
	}
	if !self.IsSealed() {
		t.Fatal("the self-managed tenant was unsealed by the node unseal")
	}
}

// TestClusteredNode_ClusterSealSealsTheWholeNode: on a 3-node cluster with
// cluster sealing active, sealing the node barrier through one node seals
// every tenant barrier on every node.
func TestClusteredNode_ClusterSealSealsTheWholeNode(t *testing.T) {
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
		addBothTenantKinds(t, n)
	}

	if err := nodes[0].Client().Barrier().BarrierSeal(ctx); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}
	for i, n := range nodes {
		for {
			st, err := n.Client().Barrier().BarrierStatus(ctx)
			if err == nil && st.Sealed {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatalf("node %d never sealed: %v", replicas[i].id, err)
			case <-time.After(clusterPollInterval):
			}
		}
		requireWholeNodeSealed(t, n, "node "+strconv.FormatUint(replicas[i].id, 10))
	}
}

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
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// clusterFormationBudget bounds how long a three-replica cluster may take to
// elect a leader and commit one write. It is a failure bound, not a pacing
// interval: every wait below returns the moment its condition holds, so an
// idle machine never spends it and a loaded one gets all of it (go-qrdb#418).
const clusterFormationBudget = 2 * time.Minute

// clusterPollInterval is how often a condition wait re-checks its condition.
const clusterPollInterval = 25 * time.Millisecond

// formationReplica is one member of an in-process test cluster: its replica
// ID, Raft address and transport certificate.
type formationReplica struct {
	id   uint64
	addr string
	cert *qrdbdharness.KeyPair
}

// newFormationReplicas issues one leaf certificate per replica from a shared
// test CA and reserves one UDP port per replica for its Raft transport.
func newFormationReplicas(t *testing.T, n int) (*qrdbdharness.CA, []formationReplica) {
	t.Helper()
	pki := t.TempDir()
	ca, err := qrdbdharness.NewCA(pki)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	ports, err := qrdbdharness.ReserveUDPPorts(n)
	if err != nil {
		t.Fatalf("ReserveUDPPorts: %v", err)
	}
	replicas := make([]formationReplica, n)
	for i := range replicas {
		id := uint64(i + 1)
		name := "node-" + strconv.FormatUint(id, 10)
		kp, err := ca.Issue(filepath.Join(pki, name),
			qrdbdharness.WithSubject(name, "raft"), qrdbdharness.WithServerAuth())
		if err != nil {
			t.Fatalf("Issue(%s): %v", name, err)
		}
		replicas[i] = formationReplica{id: id, addr: fmt.Sprintf("127.0.0.1:%d", ports[i]), cert: kp}
	}
	return ca, replicas
}

// formationConfig is a seal-disabled, network-protocol-free clustered config:
// the only socket a replica binds is its Raft transport.
func formationConfig(t *testing.T, r formationReplica, memberIdentities string) *qrdbconfig.Config {
	t.Helper()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Raft.ReplicaID = r.id
	cfg.Raft.RaftAddress = r.addr
	cfg.Raft.ListenAddress = r.addr
	cfg.Raft.MemberIdentities = memberIdentities
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
	cfg.Seal.Enabled = false
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	return cfg
}

// peerIdentitySpecs renders the raft.member_identities value a replica needs:
// the identity of every OTHER member, derived from that member's certificate
// file exactly as an operator would (qrdbd node-identity). The local entry is
// left out on purpose: the node derives it from its own certificate.
func peerIdentitySpecs(t *testing.T, self uint64, replicas []formationReplica) string {
	t.Helper()
	var specs []string
	for _, r := range replicas {
		if r.id == self {
			continue
		}
		identity, err := MemberIdentityFromPEM(r.cert.CertPEM)
		if err != nil {
			t.Fatalf("MemberIdentityFromPEM(%d): %v", r.id, err)
		}
		specs = append(specs, strconv.FormatUint(r.id, 10)+"="+identity)
	}
	return strings.Join(specs, ",")
}

// bootFormationCluster builds every replica through NewClusteredNode
// concurrently — a founder's StartShard does not wait for its peers, but
// building them in parallel is how a real cluster comes up — and registers
// their Close. identitiesFor returns each replica's raft.member_identities.
func bootFormationCluster(t *testing.T, ca *qrdbdharness.CA, replicas []formationReplica, identitiesFor func(uint64) string) ([]*Node, error) {
	t.Helper()
	caPEM := mustReadCAPEM(t, ca)
	members := make(map[uint64]string, len(replicas))
	for _, r := range replicas {
		members[r.id] = r.addr
	}

	nodes := make([]*Node, len(replicas))
	errs := make([]error, len(replicas))
	var wg sync.WaitGroup
	for i, r := range replicas {
		// Built on the test goroutine: the helpers may t.Fatal.
		cfg := formationConfig(t, r, identitiesFor(r.id))
		mtls := &RaftMTLSConfig{CACert: caPEM, Cert: r.cert.CertPEM, Key: r.cert.KeyPEM}
		wg.Add(1)
		go func() {
			defer wg.Done()
			nodes[i], errs[i] = NewClusteredNode(context.Background(), cfg, members, nil, WithRaftMTLS(mtls))
		}()
	}
	wg.Wait()
	for _, n := range nodes {
		if n != nil {
			t.Cleanup(func() { _ = n.Close() })
		}
	}
	return nodes, errors.Join(errs...)
}

// mustReadCAPEM returns the CA certificate PEM the harness wrote.
func mustReadCAPEM(t *testing.T, ca *qrdbdharness.CA) []byte {
	t.Helper()
	pem, err := os.ReadFile(ca.CertPath)
	if err != nil {
		t.Fatalf("read CA certificate: %v", err)
	}
	return pem
}

// awaitAgreedLeader waits until every host reports the SAME leader for the KV
// shard and returns it. Agreement, not merely "some host has a leader", is the
// formation property: a replica that refused its peers' traffic would never
// learn the leader the others elected.
func awaitAgreedLeader(t *testing.T, nodes []*Node) uint64 {
	t.Helper()
	deadline := time.Now().Add(clusterFormationBudget)
	for {
		leader, agreed := uint64(0), true
		for _, n := range nodes {
			host, ok := n.host.(*quicraft.Host)
			if !ok {
				t.Fatalf("node host is %T, want *quicraft.Host", n.host)
			}
			id, known, err := host.GetLeaderID(shard.ShardKV)
			if err != nil || !known || (leader != 0 && id != leader) {
				agreed = false
				break
			}
			leader = id
		}
		if agreed {
			return leader
		}
		if time.Now().After(deadline) {
			t.Fatalf("replicas did not agree on a leader within %s", clusterFormationBudget)
		}
		time.Sleep(clusterPollInterval)
	}
}

// TestNewClusteredNode_ThreeReplicasFormAndReplicate is go-qrdb#556's
// verification: three replicas built through the SDK constructor, each with
// its own certificate from one CA, form a cluster, agree on a leader and
// replicate a write. On go-quicraft >= 0.13 a founder whose shard config names
// no member identities is refused at StartShard, so this fails without the
// identities buildClusteredRaftConfigs resolves.
func TestNewClusteredNode_ThreeReplicasFormAndReplicate(t *testing.T) {
	ca, replicas := newFormationReplicas(t, 3)
	nodes, err := bootFormationCluster(t, ca, replicas, func(self uint64) string {
		return peerIdentitySpecs(t, self, replicas)
	})
	if err != nil {
		t.Fatalf("NewClusteredNode: %v", err)
	}

	// The identity the node derives for itself must be the one its transport
	// presents; otherwise every peer would drop its traffic.
	for i, n := range nodes {
		want, err := cluster.MemberIdentityFromPEM(replicas[i].cert.CertPEM)
		if err != nil {
			t.Fatalf("MemberIdentityFromPEM: %v", err)
		}
		if got := n.host.(*quicraft.Host).NodeIdentity(); got != want {
			t.Fatalf("replica %d: Host.NodeIdentity() = %q, derived %q", replicas[i].id, got, want)
		}
	}

	leader := awaitAgreedLeader(t, nodes)
	leaderNode := nodes[leader-1]

	ctx, cancel := context.WithTimeout(context.Background(), clusterFormationBudget)
	defer cancel()
	if err := putUntilCommitted(ctx, leaderNode.Client().KV(), "formation/probe", []byte("formed")); err != nil {
		t.Fatalf("write through leader %d: %v", leader, err)
	}
	for i, n := range nodes {
		got, err := n.Client().KV().Get(ctx, "formation/probe")
		if err != nil {
			t.Fatalf("replica %d read: %v", replicas[i].id, err)
		}
		if string(got) != "formed" {
			t.Fatalf("replica %d read %q, want %q", replicas[i].id, got, "formed")
		}
	}
}

// TestNewClusteredNode_MissingPeerIdentityRefused pins the refusal: a founder
// that names no identity for a peer does not start at all, rather than
// starting and dropping that peer's traffic forever.
func TestNewClusteredNode_MissingPeerIdentityRefused(t *testing.T) {
	ca, replicas := newFormationReplicas(t, 3)
	caPEM := mustReadCAPEM(t, ca)
	members := make(map[uint64]string, len(replicas))
	for _, r := range replicas {
		members[r.id] = r.addr
	}
	r := replicas[0]
	cfg := formationConfig(t, r, "")
	mtls := &RaftMTLSConfig{CACert: caPEM, Cert: r.cert.CertPEM, Key: r.cert.KeyPEM}
	n, err := NewClusteredNode(context.Background(), cfg, members, nil, WithRaftMTLS(mtls))
	if err == nil {
		_ = n.Close()
		t.Fatal("NewClusteredNode succeeded with no peer identities; want a refusal")
	}
	var invalid *quicraft.InvalidMemberError
	if !errors.As(err, &invalid) {
		t.Fatalf("error = %v (%T), want it to wrap *quicraft.InvalidMemberError", err, err)
	}
}

// TestNewClusteredNode_MismatchedSelfIdentityRefused pins the local check: a
// configured identity for this replica that is not its certificate's is a
// configuration error naming raft.member_identities.
func TestNewClusteredNode_MismatchedSelfIdentityRefused(t *testing.T) {
	ca, replicas := newFormationReplicas(t, 3)
	caPEM := mustReadCAPEM(t, ca)
	members := make(map[uint64]string, len(replicas))
	for _, r := range replicas {
		members[r.id] = r.addr
	}
	r := replicas[0]
	otherIdentity, err := cluster.MemberIdentityFromPEM(replicas[1].cert.CertPEM)
	if err != nil {
		t.Fatalf("MemberIdentityFromPEM: %v", err)
	}
	cfg := formationConfig(t, r, "1="+otherIdentity+","+peerIdentitySpecs(t, r.id, replicas))
	mtls := &RaftMTLSConfig{CACert: caPEM, Cert: r.cert.CertPEM, Key: r.cert.KeyPEM}
	n, err := NewClusteredNode(context.Background(), cfg, members, nil, WithRaftMTLS(mtls))
	if err == nil {
		_ = n.Close()
		t.Fatal("NewClusteredNode succeeded with a wrong self identity; want a refusal")
	}
	var mismatch *cluster.MemberIdentityMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("error = %v (%T), want it to wrap *cluster.MemberIdentityMismatchError", err, err)
	}
	var cfgErr *ConfigError
	if !errors.As(err, &cfgErr) || cfgErr.Field != "raft.member_identities" {
		t.Fatalf("error = %v, want a *ConfigError for raft.member_identities", err)
	}
}

// putUntilCommitted writes key through kv, retrying only the refusals a
// just-elected leader legitimately returns while it settles (leadership moved,
// or the read index is not yet established). Any other error is returned at
// once. It gives up when ctx ends.
func putUntilCommitted(ctx context.Context, kv transport.KVClient, key string, value []byte) error {
	for {
		err := kv.Put(ctx, key, value)
		if err == nil {
			return nil
		}
		var notLeader *transport.NotLeaderError
		if !errors.As(err, &notLeader) && !errors.Is(err, quicraft.ErrReadIndexNotReady) {
			return err
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), err)
		case <-time.After(clusterPollInterval):
		}
	}
}

// TestRunningCluster_AddReplica_Identity drives go-qrdb#556's add paths on a
// real three-replica cluster built through NewClusteredNode:
//
//   - a brand-new fourth node joins when the operator supplies its identity
//     (the override) — the leader commits it with the replica, and the new
//     replica catches up and serves a write;
//   - an override that contradicts the identity already committed for a
//     replica is refused with a typed error;
//   - a new node with no identity, which no committed membership names, is
//     refused with a typed error that says how to get one.
func TestRunningCluster_AddReplica_Identity(t *testing.T) {
	ca, replicas := newFormationReplicas(t, 4)
	founders := replicas[:3]
	nodes, err := bootFormationCluster(t, ca, founders, func(self uint64) string {
		return peerIdentitySpecs(t, self, founders)
	})
	if err != nil {
		t.Fatalf("NewClusteredNode: %v", err)
	}
	leader := awaitAgreedLeader(t, nodes)
	admin := nodes[leader-1].Client().Admin()

	ctx, cancel := context.WithTimeout(context.Background(), clusterFormationBudget)
	defer cancel()

	t.Run("override contradicting a committed identity is refused", func(t *testing.T) {
		stranger, err := MemberIdentityFromPEM(replicas[3].cert.CertPEM)
		if err != nil {
			t.Fatalf("MemberIdentityFromPEM: %v", err)
		}
		err = admin.AddMember(ctx, shard.ShardKV, 2, replicas[1].addr, stranger)
		var conflict *cluster.MemberIdentityConflictError
		if !errors.As(err, &conflict) || conflict.ReplicaID != 2 {
			t.Fatalf("AddMember = %v, want *cluster.MemberIdentityConflictError for replica 2", err)
		}
	})

	t.Run("new node without an identity is refused", func(t *testing.T) {
		err := admin.AddMember(ctx, shard.ShardKV, 9, "127.0.0.1:9", "")
		var unknown *cluster.MemberIdentityUnknownError
		if !errors.As(err, &unknown) || unknown.ReplicaID != 9 {
			t.Fatalf("AddMember = %v, want *cluster.MemberIdentityUnknownError for replica 9", err)
		}
		if !strings.Contains(err.Error(), "qrdbd node-identity --cert") {
			t.Fatalf("refusal must say how to get the identity: %v", err)
		}
	})

	t.Run("fourth replica joins with an operator-supplied identity", func(t *testing.T) {
		fourth := replicas[3]
		identity, err := MemberIdentityFromPEM(fourth.cert.CertPEM)
		if err != nil {
			t.Fatalf("MemberIdentityFromPEM: %v", err)
		}
		if err := admin.AddMember(ctx, shard.ShardKV, fourth.id, fourth.addr, identity); err != nil {
			t.Fatalf("AddMember(4) with its identity: %v", err)
		}

		// The joiner names the founders as the replicas it accepts its
		// bootstrap from.
		cfg := formationConfig(t, fourth, peerIdentitySpecs(t, fourth.id, replicas))
		cfg.Raft.Join = true
		members := make(map[uint64]string, len(replicas))
		for _, r := range replicas {
			members[r.id] = r.addr
		}
		mtls := &RaftMTLSConfig{CACert: mustReadCAPEM(t, ca), Cert: fourth.cert.CertPEM, Key: fourth.cert.KeyPEM}
		joiner, err := NewClusteredNode(context.Background(), cfg, members, nil, WithRaftMTLS(mtls))
		if err != nil {
			t.Fatalf("NewClusteredNode(joiner): %v", err)
		}
		t.Cleanup(func() { _ = joiner.Close() })

		if err := putUntilCommitted(ctx, nodes[leader-1].Client().KV(), "formation/fourth", []byte("joined")); err != nil {
			t.Fatalf("write through the leader: %v", err)
		}
		// The joiner serves the write once it has caught up from the leader.
		deadline := time.Now().Add(clusterFormationBudget)
		for {
			got, err := joiner.Client().KV().Get(ctx, "formation/fourth")
			if err == nil && string(got) == "joined" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the fourth replica never served the write (last: %q, %v)", got, err)
			}
			time.Sleep(clusterPollInterval)
		}

		m, err := nodes[leader-1].host.(*quicraft.Host).SyncGetShardMembership(ctx, shard.ShardKV)
		if err != nil {
			t.Fatalf("SyncGetShardMembership: %v", err)
		}
		if m.Identities[fourth.id] != identity {
			t.Fatalf("committed identity for replica 4 = %q, want %q", m.Identities[fourth.id], identity)
		}
	})
}

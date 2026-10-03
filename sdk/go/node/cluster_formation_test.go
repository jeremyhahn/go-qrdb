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
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
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
	return bootFormationClusterWith(t, ca, replicas, identitiesFor, nil)
}

// bootFormationClusterWith is bootFormationCluster with configure applied to
// every replica's configuration (nil for none) and serverOpts given to every
// replica's server.
func bootFormationClusterWith(t *testing.T, ca *qrdbdharness.CA, replicas []formationReplica, identitiesFor func(uint64) string, configure func(*qrdbconfig.Config), serverOpts ...server.ServerOption) ([]*Node, error) {
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
		if configure != nil {
			configure(cfg)
		}
		mtls := &RaftMTLSConfig{CACert: caPEM, Cert: r.cert.CertPEM, Key: r.cert.KeyPEM}
		wg.Add(1)
		go func() {
			defer wg.Done()
			nodes[i], errs[i] = NewClusteredNode(context.Background(), cfg, members, serverOpts, WithRaftMTLS(mtls))
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

// TestNewClusteredNode_ServesALinearizableReadOnReturn holds the clustered
// constructor to the host-open contract every other path keeps: a node is
// handed out only once its system shard can serve a linearizable read, so the
// caller's FIRST read succeeds -- no leader poll, no retry. Three founders boot
// concurrently, as an operator's do, and nothing waits for an election before
// each returned node is asked for one ReadIndex. Without the wait the founders
// return within milliseconds of StartShard, seconds before any election
// timeout fires, and the probe finds no leader -- or a leader that has not yet
// committed in its term (quicraft.ErrReadIndexNotReady), which is what a
// replica add's committed-membership read hit. Tenant isolation is off: the
// wait must not ride on that option.
func TestNewClusteredNode_ServesALinearizableReadOnReturn(t *testing.T) {
	ca, replicas := newFormationReplicas(t, 3)
	nodes, err := bootFormationCluster(t, ca, replicas, func(self uint64) string {
		return peerIdentitySpecs(t, self, replicas)
	})
	if err != nil {
		t.Fatalf("NewClusteredNode: %v", err)
	}
	for i, n := range nodes {
		if err := readIndexOnce(n.host.(*quicraft.Host)); err != nil {
			t.Fatalf("replica %d: first linearizable read after NewClusteredNode returned: %v", replicas[i].id, err)
		}
	}
}

// readIndexOnce issues exactly one linearizable ReadIndex against the system
// shard and reports its outcome, bounded by firstReadBudget.
func readIndexOnce(host *quicraft.Host) error {
	ctx, cancel := context.WithTimeout(context.Background(), firstReadBudget)
	defer cancel()
	rs, err := host.ReadIndex(ctx, shard.ShardKV)
	if err != nil {
		return err
	}
	defer rs.Release()
	_, err = rs.Result()
	return err
}

// firstReadBudget bounds the single probe readIndexOnce issues: a round trip
// to a healthy leader with headroom for the race detector, and well under the
// founders' election timeout, so it cannot cover for a missing wait by
// outlasting an election.
const firstReadBudget = time.Second

// TestNewClusteredNode_CancelledContextReturnsPromptly: a founder waits for a
// quorum to elect and commit before NewClusteredNode returns, and that wait
// honours the caller's ctx. One founder of three is started alone -- no quorum
// can form -- and its ctx is cancelled shortly after: the constructor returns
// the cancellation well inside the readiness budget instead of holding the
// caller for all of it.
func TestNewClusteredNode_CancelledContextReturnsPromptly(t *testing.T) {
	ca, replicas := newFormationReplicas(t, 3)
	caPEM := mustReadCAPEM(t, ca)
	members := make(map[uint64]string, len(replicas))
	for _, r := range replicas {
		members[r.id] = r.addr
	}
	r := replicas[0]
	cfg := formationConfig(t, r, peerIdentitySpecs(t, r.id, replicas))
	mtls := &RaftMTLSConfig{CACert: caPEM, Cert: r.cert.CertPEM, Key: r.cert.KeyPEM}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	n, err := NewClusteredNode(ctx, cfg, members, nil, WithRaftMTLS(mtls))
	elapsed := time.Since(start)
	if n != nil {
		_ = n.Close()
		t.Fatal("NewClusteredNode returned a node although no quorum could form")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed > cancelledBootBudget {
		t.Fatalf("NewClusteredNode returned %s after its ctx was cancelled at 300ms; want within %s", elapsed, cancelledBootBudget)
	}
}

// cancelledBootBudget is how long a cancelled clustered boot may take to
// return: the cancel at 300ms plus one bounded read probe and teardown, far
// below the 15s readiness budget it would otherwise spend.
const cancelledBootBudget = 5 * time.Second

// TestClusteredNode_UnsealRetriesAfterACancelledBoot: the clustered boot that
// Unseal runs honours Unseal's ctx, so a caller that gives up -- here, one
// founder unsealed alone, its ctx cancelled before a quorum exists -- gets the
// cancellation, and the node stays bootable: the next Unseal completes the
// boot instead of reporting an unsealed barrier as a booted node. All three
// founders are then unsealed together and the retried node serves a write.
func TestClusteredNode_UnsealRetriesAfterACancelledBoot(t *testing.T) {
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
	creds := func() seal.Credentials { return seal.MustCredentials([]byte("wal-encryption-test")) }

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	if err := nodes[0].Unseal(ctx, creds()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Unseal of a lone founder with its ctx cancelled: err = %v, want context.Canceled", err)
	}
	if !nodes[0].IsSealed() {
		t.Fatal("a cancelled boot must leave the node sealed")
	}
	assertAllTenantBarriersSealed(t, nodes[0].BarrierRegistry())

	errs := make([]error, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = n.Unseal(context.Background(), creds())
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	client := nodes[0].Client()
	if client == nil {
		t.Fatal("the retried node reports unsealed but never finished booting: it has no client")
	}
	putCtx, putCancel := context.WithTimeout(context.Background(), clusterFormationBudget)
	defer putCancel()
	if err := putUntilCommitted(putCtx, client.KV(), "retried", []byte("v")); err != nil {
		t.Fatalf("Put through the retried node: %v", err)
	}
}

// assertAllTenantBarriersSealed fails the test if any barrier registered in
// reg -- a per-tenant barrier a tenant-isolation reattach may have unsealed
// before the boot that drove it was cancelled -- reports unsealed. reg may
// be nil (seal disabled or no BarrierRegistry); a nil registry trivially
// satisfies the check.
func assertAllTenantBarriersSealed(t *testing.T, reg *seal.BarrierRegistry) {
	t.Helper()
	if reg == nil {
		return
	}
	for _, id := range reg.ListTenants() {
		tb, err := reg.GetTenant(id)
		if err != nil {
			t.Fatalf("GetTenant(%s): %v", id, err)
		}
		if tb.IsSealed() {
			continue
		}
		t.Fatalf("tenant %s barrier is unsealed after a cancelled boot that reports the node sealed", id)
	}
}

// TestClusteredNode_UnsealRetriesAfterACancelledBoot_TenantBarriersSealed
// covers the tenant-isolation path: BuildTenantInfra -> RestoreVolumeTenants
// unseals every auto-unseal tenant barrier it finds in ONE pass, before
// reattachVolumeTenants (re-)starts each tenant's per-tenant Host -- a
// sequence of single-node Raft elections that, with several tenants, takes
// long enough to land a cancellation reliably inside it. A cancellation
// there must not leave a tenant's DEK live in memory while the node itself
// reports sealed, and a retried Unseal must still complete the boot.
func TestClusteredNode_UnsealRetriesAfterACancelledBoot_TenantBarriersSealed(t *testing.T) {
	ctx := context.Background()
	cfg := sealClustered(newRaftIsolationConfig(t))
	creds := func() seal.Credentials { return seal.MustCredentials([]byte("tenant-seal-cancel-test")) }
	const tenantCount = 6

	// First boot, unsealed in full: create several auto-unseal volume tenants
	// so their barrier key material and tenant-root records persist under
	// cfg.Raft.DataDir and the volume pool for the second boot to restore.
	n1, err := NewClusteredNode(ctx, cfg, raftMembers(), nil,
		WithTenantIsolation(), WithDefaultTenantEntitlements(volumeEntitlements()))
	if err != nil {
		t.Fatalf("NewClusteredNode (first boot): %v", err)
	}
	if err := n1.Unseal(ctx, creds()); err != nil {
		t.Fatalf("first boot Unseal: %v", err)
	}
	for i := 0; i < tenantCount; i++ {
		id := fmt.Sprintf("tenant-%02d", i)
		if _, err := n1.Services().Tenant.CreateTenant(ctx, id, id,
			tenant.IsolationVolume, tenant.WithBarrierAutoUnseal(true)); err != nil {
			t.Fatalf("CreateTenant(%s): %v", id, err)
		}
	}
	if err := n1.Close(); err != nil {
		t.Fatalf("Close (first boot): %v", err)
	}

	// Re-open the same data directory: the system barrier is sealed again
	// (NewClusteredNode never auto-unseals a passphrase barrier), so this
	// Unseal re-runs completeClusteredBoot from scratch, including
	// BuildTenantInfra's restore of the tenants just created, which unseals
	// every auto-unseal tenant barrier in one pass.
	//
	// The cancellation lands through tenantInfraBuiltHook, right after that
	// pass and before the rest of the boot, rather than by timing: a node
	// that installs a cluster seal monitor re-attaches no tenant Host at boot
	// (the reconciler and first access do, once the record allows), so what
	// remains of the boot after the restore is too short to land a timed
	// cancellation in reliably.
	n2, err := NewClusteredNode(ctx, cfg, raftMembers(), nil,
		WithTenantIsolation(), WithDefaultTenantEntitlements(volumeEntitlements()))
	if err != nil {
		t.Fatalf("NewClusteredNode (second boot): %v", err)
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registeredAtCancel := -1
	prevHook := tenantInfraBuiltHook
	tenantInfraBuiltHook = func() {
		registeredAtCancel = len(n2.BarrierRegistry().ListTenants())
		cancel()
	}
	unsealErr := n2.Unseal(cancelCtx, creds())
	tenantInfraBuiltHook = prevHook
	if unsealErr == nil {
		t.Fatalf("Unseal cancelled after the tenant restore completed the boot (hook saw %d tenants)", registeredAtCancel)
	}
	if registeredAtCancel != tenantCount {
		t.Fatalf("tenants registered when the boot was cancelled = %d, want %d", registeredAtCancel, tenantCount)
	}
	t.Cleanup(func() { _ = n2.Close() })

	if !n2.IsSealed() {
		t.Fatal("a cancelled boot must leave the node sealed")
	}
	assertAllTenantBarriersSealed(t, n2.BarrierRegistry())

	// The failure path must also clear the components this attempt built so
	// a retry's accessors cannot hand out a host or feature gate this failed
	// boot already closed. svcNode must not be left holding the full
	// (now-stopped) post-boot node either: BuildSealServicesOnly's seal-only
	// node has no KV service (the data plane needs a live Host), so a
	// non-nil KV here means Services() is still handing out the attempt's
	// closed data plane.
	if n2.host != nil {
		t.Fatal("a cancelled boot must clear the host so a retry does not reuse a closed one")
	}
	if n2.featureGate != nil {
		t.Fatal("a cancelled boot must clear the feature gate so a retry does not reuse a closed one")
	}
	if svc := n2.Services(); svc != nil && svc.KV != nil {
		t.Fatal("a cancelled boot must not leave Services() handing out the stopped post-boot data plane")
	}

	// The retried Unseal must still complete the boot, and every tenant
	// barrier must be unsealed again through the retry.
	if err := n2.Unseal(context.Background(), creds()); err != nil {
		t.Fatalf("retried Unseal: %v", err)
	}
	reg := n2.BarrierRegistry()
	if reg == nil {
		t.Fatal("BarrierRegistry must be present on a seal-enabled node")
	}
	for i := 0; i < tenantCount; i++ {
		id := fmt.Sprintf("tenant-%02d", i)
		tb, gerr := reg.GetTenant(id)
		if gerr != nil {
			t.Fatalf("GetTenant(%s) after the retried boot: %v", id, gerr)
		}
		if tb.IsSealed() {
			t.Fatalf("tenant %s barrier must be unsealed after the retried boot completes", id)
		}
	}
}

// nodeAuthMethodProbeName is the method name nodeAuthMethodProbe contributes
// to the auth-chain vocabulary. It is not a built-in token, so IsAuthMethod
// answers true for it only while a middleware carrying this authenticator is
// bound -- exactly the discriminator
// TestClusteredNode_UnsealRetriesAfterACancelledBoot_DataPlaneAccessorsCleared
// needs to prove n.authMethods.Bind(nil) actually ran: unlike a built-in
// token, which would read true from the zero-value vocabulary too.
const nodeAuthMethodProbeName = "node-custom-auth-probe"

// nodeAuthMethodProbe is a minimal auth.Authenticator whose only purpose is
// to name nodeAuthMethodProbeName in the AuthMiddleware's method vocabulary
// (mirrors pkg/auth/wiring's own TestBuildAuthMiddleware_ExtraAuthenticatorJoinsTheMethodVocabulary
// fixture); it never actually authenticates anything.
type nodeAuthMethodProbe struct{}

func (nodeAuthMethodProbe) Authenticate(context.Context) (*auth.Identity, error) {
	return nil, auth.ErrUnauthenticated
}

func (nodeAuthMethodProbe) Name() string { return nodeAuthMethodProbeName }

// TestClusteredNode_UnsealRetriesAfterACancelledBoot_DataPlaneAccessorsCleared
// covers a boot that fails AFTER wireClientAndServer has already published
// the data-plane service handles (AuthFactorService, UserAuthFactorService,
// ABACPolicyReloader) but before srv.Start succeeds: all three are stored
// early in wireClientAndServer (via WithFactorRegistry, buildLoginOpts and
// decorateABAC, which run before verifyExistingAdminBinding and srv.Start),
// so a failure at either of those later steps must not leave them handing
// out services built over the stopped svcNode and host. The failure is
// forced deterministically by pre-binding the REST port srv.Start needs.
func TestClusteredNode_UnsealRetriesAfterACancelledBoot_DataPlaneAccessorsCleared(t *testing.T) {
	ctx := context.Background()
	certDir := t.TempDir()

	ca, err := qrdbdharness.NewCA(certDir)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	serverKP, err := ca.Issue(certDir, qrdbdharness.WithSubject("node", ""), qrdbdharness.WithServerAuth())
	if err != nil {
		t.Fatalf("Issue server cert: %v", err)
	}
	serverCert, err := serverKP.TLSCertificate()
	if err != nil {
		t.Fatalf("TLSCertificate: %v", err)
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.NoClientCert,
		MinVersion:   tls.VersionTLS13,
	}

	// Reserve a free TCP port and keep it bound for the whole first Unseal
	// attempt: srv.Start's startREST binds exactly this cfg.Server.Host:
	// cfg.Server.AdminPort pair, so it fails with "address already in use"
	// deterministically, no timing involved.
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	port := blocker.Addr().(*net.TCPAddr).Port

	cfg := sealClustered(newRaftIsolationConfig(t))
	cfg.Protocols.REST = true
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.AdminPort = port

	reg, _ := newPopulatedFactorRegistry(t, "piv")
	serverOpts := []server.ServerOption{server.WithTLS(tlsCfg), server.WithFactorRegistry(reg)}

	n, err := NewClusteredNode(ctx, cfg, raftMembers(), serverOpts,
		WithABACLiveReload(), WithAuthenticators(nodeAuthMethodProbe{}))
	if err != nil {
		t.Fatalf("NewClusteredNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })

	creds := func() seal.Credentials { return seal.MustCredentials([]byte("accessor-cancel-test")) }

	if err := n.Unseal(ctx, creds()); err == nil {
		_ = blocker.Close()
		t.Fatal("Unseal must fail while the REST port is already bound")
	}
	if err := blocker.Close(); err != nil {
		t.Fatalf("release the blocking listener: %v", err)
	}

	if !n.IsSealed() {
		t.Fatal("a boot that fails after publishing data-plane services must leave the node sealed")
	}
	if svc := n.AuthFactorService(); svc != nil {
		t.Fatal("AuthFactorService must be nil after a failed boot, not a service built over the stopped host")
	}
	if svc := n.UserAuthFactorService(); svc != nil {
		t.Fatal("UserAuthFactorService must be nil after a failed boot, not a service built over the stopped host")
	}
	if r := n.ABACPolicyReloader(); r != nil {
		t.Fatal("ABACPolicyReloader must be nil after a failed boot, not a reloader built over the stopped host")
	}

	// Every other backing handle a failed boot built must be retracted too:
	// the scoped encryption migrator, the replicated volume-pool cursor and
	// the cluster-wide tenant-slot store (both bound by bindReplicatedVolumePool
	// before wireClientAndServer -- the failing step -- ever runs), and the
	// database manager.
	if n.encMigrator.Load() != nil {
		t.Fatal("encMigrator must be nil after a failed boot, not the scoped router built over the stopped host")
	}
	if n.volumePool == nil || n.volumePool.ReplicatedState == nil {
		t.Fatal("this test's config must configure a clustered volume pool to exercise the cursor/slot-store detach")
	}
	if n.volumePool.ReplicatedState.Bound() {
		t.Fatal("the replicated volume-pool cursor must be unbound after a failed boot")
	}
	if reg := n.BarrierRegistry(); reg == nil || reg.HasReplicatedSlotStore() {
		t.Fatal("the cluster-wide tenant-slot store must be detached after a failed boot")
	}
	if n.dbManager != nil {
		t.Fatal("dbManager must be nil after a failed boot, not a manager over the closed data directory")
	}

	// B1: the background context driving the lease-expiry sweeper, the
	// reclaim sweeper and (when tenant isolation wires one) the tenant shard
	// reconciler must be CLEARED alongside bgCancel, not merely cancelled:
	// background() makes a fresh context only when bgCtx is nil, so leaving
	// bgCtx set to the cancelled one would hand a retried boot's goroutines a
	// context that is already Done, and they would exit at once -- no lease
	// reaping (go-qrdb#174 regressing), no reclaim sweep, no reconcile.
	if n.bgCtx != nil {
		t.Fatal("bgCtx must be cleared alongside bgCancel after a failed boot, not left holding the cancelled context")
	}

	// n.authMethods was bound to the AuthMiddleware wireClientAndServer built
	// over this attempt's svcNode (n.authMethods.Bind(authMW)); the failure
	// defer must unbind it (Bind(nil)) so it falls back to the built-in
	// tokens instead of consulting a middleware whose Role/User services are
	// now stopped. nodeAuthMethodProbeName is not a built-in token, so this
	// is false only while unbound -- a built-in token would read true either
	// way and prove nothing.
	if n.authMethods.IsAuthMethod(nodeAuthMethodProbeName) {
		t.Fatal("authMethods must fall back to the built-in tokens after a failed boot, not consult the middleware built over the stopped host")
	}

	// A failed boot must not leave the node able to place or allocate a new
	// tenant through the now-local-only path while it reports sealed. The
	// seal-only Services().Tenant this attempt's defer restored is backed by
	// the system barrier itself (tenant.NewTenantService(infra.SealBarrier,
	// ...)), which refuses every read and write while sealed (seal.ErrSealed)
	// -- CreateTenant's tombstone check is the first such read, so it fails
	// closed before any pool slot is picked or any tenant barrier is opened.
	svc := n.Services()
	if svc == nil || svc.Tenant == nil {
		t.Fatal("Services().Tenant must still be reachable (the seal-only snapshot) after a failed boot")
	}
	if _, cerr := svc.Tenant.CreateTenant(ctx, "probe-tenant", "Probe", tenant.IsolationVolume); !errors.Is(cerr, seal.ErrSealed) {
		t.Fatalf("CreateTenant while sealed = %v, want seal.ErrSealed: nothing may place or allocate while sealed", cerr)
	}

	// The retried Unseal, with the port now free, must complete the boot and
	// republish all three accessors.
	if err := n.Unseal(context.Background(), creds()); err != nil {
		t.Fatalf("retried Unseal: %v", err)
	}
	if svc := n.AuthFactorService(); svc == nil {
		t.Fatal("AuthFactorService must be non-nil once the retried boot completes")
	}
	if svc := n.UserAuthFactorService(); svc == nil {
		t.Fatal("UserAuthFactorService must be non-nil once the retried boot completes")
	}
	if r := n.ABACPolicyReloader(); r == nil {
		t.Fatal("ABACPolicyReloader must be non-nil once the retried boot completes")
	}
	if !n.authMethods.IsAuthMethod(nodeAuthMethodProbeName) {
		t.Fatal("authMethods must be rebound to the retried boot's middleware once it completes")
	}

	// B1: the retried boot's background goroutines must run on a live
	// context, not the dead one a cleared bgCancel over an uncleared bgCtx
	// would have handed them.
	if n.bgCtx == nil || n.bgCtx.Err() != nil {
		t.Fatalf("bgCtx after the retried boot: got %v, want a live (non-Done) context", n.bgCtx)
	}

	// Prove the lease-expiry sweeper the retry started is actually running,
	// not merely holding a live context: grant a 1s lease, attach a key to
	// it, and poll for the leader-side sweep to revoke it through consensus.
	// Without the B1 fix the sweeper exits at once on the dead bgCtx a retry
	// would otherwise have reused, and this key would never be reaped
	// (go-qrdb#174, the defect this whole fix chain guards against).
	client := n.Client()
	if client == nil {
		t.Fatal("Client() must be non-nil once the retried boot completes")
	}
	leaseInfo, lerr := client.Lease().LeaseGrant(ctx, 1)
	if lerr != nil {
		t.Fatalf("LeaseGrant: %v", lerr)
	}
	const probeKey = "rr-followups/lease-sweep-probe"
	if _, perr := client.MVCC().PutWithLease(ctx, probeKey, []byte("v"), leaseInfo.ID); perr != nil {
		t.Fatalf("PutWithLease: %v", perr)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, gerr := client.KV().Get(ctx, probeKey); gerr != nil {
			var notFound *transport.KeyNotFoundError
			if errors.As(gerr, &notFound) {
				break // the key is gone: the lease was revoked through consensus.
			}
			t.Fatalf("Get(%s): %v", probeKey, gerr)
		}
		if time.Now().After(deadline) {
			t.Fatal("the lease-expiry sweeper never reaped an expired lease's key after the retried boot -- go-qrdb#174 regressed")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

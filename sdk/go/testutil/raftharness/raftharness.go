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

// Package raftharness builds in-process go-quicraft hosts WITH Raft
// transports whose system shard is formed — the clustered daemon's shape —
// for tests of the paths that add, promote and found replicas, which
// go-quicraft subjects to its member-identity rule only on a host with a
// transport. Every host listens on loopback and lives in the test's temporary
// directories.
package raftharness

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// Bound bounds each wait the harness and its callers make. A failure bound,
// not a pacing interval: every wait returns once its condition holds.
const Bound = 2 * time.Minute

// pollAttempt bounds a single readiness probe inside WaitReadable. It must
// stay well below Bound: SyncGetShardMembership blocks for its whole context
// budget while no leader has confirmed a read index yet (go-quicraft's
// ReadIndex only returns once it is satisfied or ctx expires), so giving an
// attempt the full Bound turns the retry loop into one very long call --  a
// single slow election round consumes the entire budget and WaitReadable
// fails having tried exactly once, indistinguishable from never becoming
// ready at all. A short per-attempt timeout lets the loop send a fresh
// ReadIndex request every pollAttempt, so a lost round or a slow election
// under scheduling pressure gets retried throughout the whole Bound window
// instead of spending it on a single attempt.
const pollAttempt = 2 * time.Second

// Cluster is n hosts whose system shard (shard.ShardKV) is formed with every
// host a voter, so each host's identity is committed in the system shard's
// membership. Host i runs system-shard replica i+1.
type Cluster struct {
	// Hosts are the shared Raft hosts, in replica order.
	Hosts []*quicraft.Host
	// Addrs are the hosts' Raft addresses.
	Addrs []string
	// Identities maps each system-shard replica ID to its host's identity.
	Identities map[uint64]string

	createFn sm.CreateFunc
}

// NewCluster starts n hosts and forms their system shard, waiting until every
// host serves a linearizable read of its membership. The hosts are closed when
// the test ends.
func NewCluster(t testing.TB, n int) *Cluster {
	t.Helper()
	pki := t.TempDir()
	ca, err := qrdbdharness.NewCA(pki)
	if err != nil {
		t.Fatalf("raftharness: NewCA: %v", err)
	}
	caPEM, err := os.ReadFile(ca.CertPath)
	if err != nil {
		t.Fatalf("raftharness: read CA: %v", err)
	}
	ports, err := qrdbdharness.ReserveUDPPorts(n)
	if err != nil {
		t.Fatalf("raftharness: ReserveUDPPorts: %v", err)
	}
	createFn, err := statemachine.ResolveTyped("memory", "")
	if err != nil {
		t.Fatalf("raftharness: ResolveTyped(memory): %v", err)
	}
	c := &Cluster{Identities: map[uint64]string{}, createFn: createFn}
	members := map[uint64]string{}
	certs := make([]*qrdbdharness.KeyPair, n)
	for i := range n {
		name := "node-" + strconv.Itoa(i+1)
		kp, err := ca.Issue(filepath.Join(pki, name), qrdbdharness.WithSubject(name, "raft"), qrdbdharness.WithServerAuth())
		if err != nil {
			t.Fatalf("raftharness: Issue: %v", err)
		}
		certs[i] = kp
		addr := "127.0.0.1:" + strconv.Itoa(ports[i])
		c.Addrs = append(c.Addrs, addr)
		members[uint64(i+1)] = addr
		if c.Identities[uint64(i+1)], err = cluster.MemberIdentityFromPEM(kp.CertPEM); err != nil {
			t.Fatalf("raftharness: MemberIdentityFromPEM: %v", err)
		}
	}
	for i := range n {
		dir := t.TempDir()
		cfg := quicraftconfig.HostConfig{
			WALDir:        filepath.Join(dir, "wal"),
			NodeHostDir:   filepath.Join(dir, "nodehost"),
			RaftAddress:   c.Addrs[i],
			ListenAddress: c.Addrs[i],
			DeploymentID:  1,
		}
		cfg.TransportConfig.MTLSConfig = &quicraftconfig.MTLSConfig{CACert: caPEM, Cert: certs[i].CertPEM, Key: certs[i].KeyPEM}
		host, err := quicraft.NewHost(cfg, quicraft.WithoutEncryption())
		if err != nil {
			t.Fatalf("raftharness: NewHost: %v", err)
		}
		t.Cleanup(func() { _ = host.Close() })
		if err := host.StartShard(members, false, createFn, quicraftconfig.Config{
			ShardID: shard.ShardKV, ReplicaID: uint64(i + 1), MemberIdentities: c.Identities,
		}); err != nil {
			t.Fatalf("raftharness: StartShard(system): %v", err)
		}
		c.Hosts = append(c.Hosts, host)
	}
	// Callers resolve identities against the system shard at once, so wait
	// until it serves a linearizable read — an election alone is not enough.
	c.WaitReadable(t, shard.ShardKV, c.Hosts...)
	return c
}

// CreateFn is the in-memory state machine every shard the harness starts runs.
func (c *Cluster) CreateFn() sm.CreateFunc { return c.createFn }

// WaitReadable waits until each of hosts serves a linearizable read of
// shardID's membership. A newly elected leader refuses one until it has
// committed an entry in its term, so a test that resolves identities against
// a shard it just founded waits for this rather than for the election alone.
func (c *Cluster) WaitReadable(t testing.TB, shardID uint64, hosts ...*quicraft.Host) {
	t.Helper()
	deadline := time.Now().Add(Bound)
	for _, host := range hosts {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), pollAttempt)
			_, err := host.SyncGetShardMembership(ctx, shardID)
			cancel()
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("raftharness: shard %d membership never became readable: %v", shardID, err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

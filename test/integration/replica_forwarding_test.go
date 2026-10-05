// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

package integration_test

// Replica forwarding on real qrdbd nodes, the same command set over every
// network transport: a node that holds no replica of a database forwards a
// request for it to the database's replica, as the caller, and relays the
// answer.

import (
	"strconv"
	"testing"
	"time"
)

// fwdCaller names who makes a request.
type fwdCaller int

const (
	// fwdAsWriter may read and write keys.
	fwdAsWriter fwdCaller = iota
	// fwdAsNoKV may only list databases.
	fwdAsNoKV
	// fwdAsOtherTenant belongs to a tenant that does not exist.
	fwdAsOtherTenant
)

// certFor returns the client certificate and key of caller.
func (c *fwdCluster) certFor(caller fwdCaller) (string, string) {
	switch caller {
	case fwdAsNoKV:
		return c.noKVCert, c.noKVKey
	case fwdAsOtherTenant:
		return c.otherCert, c.otherKey
	}
	return c.writerCert, c.writerKey
}

// fwdAnswer is a transport's answer, in the terms the suite asserts on.
type fwdAnswer struct {
	// ok is a success; value is a read's value.
	ok    bool
	value string
	// servedBy is the replica the answer says served a forward, empty when
	// the node served the request itself.
	servedBy string
	// refusal is the NOT_A_REPLICA refusal's metadata, nil for any other
	// answer; denied is a permission refusal; notFound a missing key.
	refusal  map[string]string
	denied   bool
	notFound bool
	// detail describes the answer for a failure message.
	detail string
}

// fwdTransport is one network transport's client side of the suite.
type fwdTransport interface {
	name() string
	// reportsServedBy says whether the transport's answer names the replica
	// that served a forward (x-qrdb-forwarded-to). One that cannot is
	// checked by the success alone: a node holding no replica refuses
	// every request it does not forward.
	reportsServedBy() bool
	// hasStream says whether watch is a stream, which is refused, or a
	// bounded request (MCP's qrdb_watch), which is forwarded like any other.
	hasStream() bool
	put(t *testing.T, c *fwdCluster, node int, caller fwdCaller, db, key, value string) fwdAnswer
	get(t *testing.T, c *fwdCluster, node int, caller fwdCaller, db, key string) fwdAnswer
	// watch opens the transport's stream on db and returns its first answer.
	watch(t *testing.T, c *fwdCluster, node int, db string) fwdAnswer
}

// fwdTransports is every network transport the suite runs over.
var fwdTransports = []fwdTransport{grpcFwdTransport{}, restFwdTransport{}, quicFwdTransport{}, mcpFwdTransport{}, grpcFwdTransport{unix: true}}

// TestReplicaForwarding: three nodes and a single-replica database, so two
// nodes hold no replica of it; the same command set over every transport.
//
// The cluster is BYO-TLS (ruling 5): every node runs on the operator's CA,
// certificate and key, so it has no cluster CA key and its cluster CA is the
// operator's peer CA. Every forward here passes the on-behalf gate's
// IsClusterCA check against that CA; the refusal of a leaf another CA issued
// is TestOnBehalf_OnlyTheClusterCAsNodeMayForward (pkg/auth/middleware).
func TestReplicaForwarding(t *testing.T) {
	c := startFwdCluster(t, 3)
	const db = "fwd-one"
	shard := c.createDatabase(t, db, 1)
	p := c.placementOf(t, shard, 1)
	replica := p.leader
	others := c.nonReplicas(p)
	replicaID := strconv.FormatUint(p.leaderRID, 10)
	t.Logf("database %s: shard %d on node %d (replica %s); forwarding through nodes %v", db, shard, replica+1, replicaID, others)

	for _, tr := range fwdTransports {
		t.Run(tr.name(), func(t *testing.T) {
			t.Run("writes and reads through a non-replica reach the replica", func(t *testing.T) {
				for _, i := range others {
					key, value := tr.name()+"-via-"+strconv.Itoa(i+1), "v"+strconv.Itoa(i+1)
					if a := tr.put(t, c, i, fwdAsWriter, db, key, value); !a.ok || (tr.reportsServedBy() && a.servedBy != replicaID) {
						t.Fatalf("put through non-replica node %d = %s, served by %q; want replica %s", i+1, a.detail, a.servedBy, replicaID)
					}
				}
				for _, i := range c.live() {
					for _, j := range others {
						key, want := tr.name()+"-via-"+strconv.Itoa(j+1), "v"+strconv.Itoa(j+1)
						if a := tr.get(t, c, i, fwdAsWriter, db, key); !a.ok || a.value != want {
							t.Fatalf("get through node %d of the write via node %d = %s %q; want %q", i+1, j+1, a.detail, a.value, want)
						}
					}
				}
				if a := tr.put(t, c, replica, fwdAsWriter, db, tr.name()+"-direct", "v"); !a.ok || a.servedBy != "" {
					t.Fatalf("put through the replica = %s, served by %q; want served directly", a.detail, a.servedBy)
				}
			})

			t.Run("the caller's own authorization applies", func(t *testing.T) {
				key := tr.name() + "-denied"
				if a := tr.put(t, c, others[0], fwdAsNoKV, db, key, "v"); !a.denied {
					t.Fatalf("put by a caller without kv:put = %s; want a permission refusal", a.detail)
				}
				if a := tr.put(t, c, others[0], fwdAsOtherTenant, db, key, "v"); a.ok {
					t.Fatal("a caller from another tenant wrote through the non-replica")
				}
				if a := tr.get(t, c, replica, fwdAsWriter, db, key); !a.notFound {
					t.Fatalf("get of the refused write = %s; want not found", a.detail)
				}
			})

			t.Run("a watch stream is refused with the replica set, a bounded watch forwarded", func(t *testing.T) {
				a := tr.watch(t, c, others[0], db)
				if !tr.hasStream() {
					if !a.ok || a.servedBy != replicaID {
						t.Fatalf("bounded watch through a non-replica = %s, served by %q; want it forwarded to replica %s", a.detail, a.servedBy, replicaID)
					}
					return
				}
				if a.refusal == nil || a.refusal["replicas"] != "1" || a.refusal["replica_0_raft_addr"] != c.nodes[replica].raftAddr {
					t.Fatalf("watch through a non-replica = %s; want the refusal naming the replica on node %d", a.detail, replica+1)
				}
			})
		})
	}

	c.stop(t, replica)
	for _, tr := range fwdTransports {
		t.Run(tr.name()+"/with the replica down the non-replica refuses with the replica set", func(t *testing.T) {
			// A forward already bound for the replica as it died fails with the
			// transport's unavailability -- what it did there is unknown -- and
			// the next finds it unreachable and refuses. No answer is a local one.
			deadline := time.Now().Add(15 * time.Second)
			for {
				a := tr.put(t, c, others[0], fwdAsWriter, db, tr.name()+"-after-stop", "v")
				if a.ok {
					t.Fatalf("put with the replica down succeeded (%s); a non-replica must never answer locally", a.detail)
				}
				if a.refusal != nil {
					if a.refusal["replica_0_raft_addr"] != c.nodes[replica].raftAddr {
						t.Fatalf("refusal %v does not name the stopped replica", a.refusal)
					}
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("put with the replica down = %s; want the refusal naming it", a.detail)
				}
				time.Sleep(200 * time.Millisecond)
			}
		})
	}
}

// TestReplicaForwarding_KillTheLeader: four nodes, a three-replica database,
// so one node holds no replica. The database's leader is killed; once the
// group elects another, the next request forwarded through the non-replica,
// over every transport, reaches the new leader: it is served by a surviving
// replica -- never the dead one, even when the leader cache still names it
// -- and the new leader holds the write.
//
// A follower serves a write by proposing it to its leader, so the replica
// that answers a forward need not be the leader itself; what reaches the new
// leader is the write.
func TestReplicaForwarding_KillTheLeader(t *testing.T) {
	c := startFwdCluster(t, 4)
	const db = "fwd-ha"
	shard := c.createDatabase(t, db, 3)
	p := c.placementOf(t, shard, 3)
	others := c.nonReplicas(p)
	if len(others) != 1 {
		t.Fatalf("non-replicas = %v; want one", others)
	}
	via := others[0]

	for _, tr := range fwdTransports {
		if a := tr.put(t, c, via, fwdAsWriter, db, tr.name()+"-before", "v1"); !a.ok || (tr.reportsServedBy() && a.servedBy == "") {
			t.Fatalf("%s: put through the non-replica = %s, served by %q; want it forwarded", tr.name(), a.detail, a.servedBy)
		}
	}
	killed := strconv.FormatUint(p.leaderRID, 10)
	t.Logf("killing leader node %d (replica %s)", p.leader+1, killed)
	c.stop(t, p.leader)
	after := c.placementOf(t, shard, 2)
	t.Logf("new leader node %d (replica %d)", after.leader+1, after.leaderRID)

	for _, tr := range fwdTransports {
		t.Run(tr.name(), func(t *testing.T) {
			a := tr.put(t, c, via, fwdAsWriter, db, tr.name()+"-after", "v2")
			if !a.ok || (tr.reportsServedBy() && (a.servedBy == "" || a.servedBy == killed)) {
				t.Fatalf("put through the non-replica after replica %s died = %s, served by %q; want a surviving replica", killed, a.detail, a.servedBy)
			}
			for key, want := range map[string]string{tr.name() + "-before": "v1", tr.name() + "-after": "v2"} {
				if a := tr.get(t, c, after.leader, fwdAsWriter, db, key); !a.ok || a.value != want {
					t.Fatalf("get %s from the new leader = %s %q; want %q", key, a.detail, a.value, want)
				}
			}
		})
	}
}

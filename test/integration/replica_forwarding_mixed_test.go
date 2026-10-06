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

// An embedded node (sdk/go/node) in a cluster of qrdbd nodes is a full member
// of raft tenant isolation: placed as a replica, it receives the tenant's key
// through the cluster key plane and serves the tenant's database; holding no
// replica, it forwards its embedded client's call to one, as the caller its
// embedder configured.

import (
	"crypto/tls"
	"strconv"
	"testing"
	"time"

	qrdbnode "github.com/jeremyhahn/go-qrdb/sdk/go/node"
)

// mixedEmbedded is the embedded node of the mixed cluster: the last of three.
const mixedEmbedded = 2

// TestMixedCluster_EmbeddedNodeServesAndForwardsARaftTenant: two qrdbd nodes
// and one embedded node. The tenant is created through a qrdbd node and its
// anchor database is placed on all three, so the embedded node can only hold
// the tenant's key if a qrdbd node delivered it.
func TestMixedCluster_EmbeddedNodeServesAndForwardsARaftTenant(t *testing.T) {
	var callerCert string
	c := startFwdClusterWith(t, 3, map[int]fwdEmbedded{
		mixedEmbedded: func(ca *testCA) []qrdbnode.NodeOption {
			certPath, keyPath := ca.generateClientCertForTenant(t, fwdTenantID)
			caller, err := tls.LoadX509KeyPair(certPath, keyPath)
			if err != nil {
				t.Fatal(err)
			}
			callerCert = certPath
			return []qrdbnode.NodeOption{qrdbnode.WithEmbeddedCaller(caller)}
		},
	})
	// auth.mtls_binding_policy defaults to "required": the configured
	// caller authenticates, and so forwards, only once enrolled as the
	// writer's binding.
	bindUserCert(t, c.sys, c.nodes[c.creator].admin, c.writerID, callerCert)
	emb := c.nodes[mixedEmbedded].embedded
	ctx := c.writerContext()
	rest := restFwdTransport{}

	t.Run("the embedded replica holds the tenant's key and serves its database", func(t *testing.T) {
		// startFwdCluster waited until every node, the embedded one
		// included, authenticated the tenant's caller over REST -- which
		// needs the tenant's key. Its embedded client reads what a qrdbd
		// node wrote, from its own replica.
		// The anchor's group may still be electing its leader: the write is
		// retried until the group commits it.
		qrdbd := 0
		if err := retry(func() error {
			if a := rest.put(t, c, qrdbd, fwdAsWriter, "fwd-anchor", "mixed/k1", "from-qrdbd"); !a.ok {
				return &fwdAnswerError{a: a}
			}
			return nil
		}); err != nil {
			t.Fatalf("Put through qrdbd node %d: %v", qrdbd+1, err)
		}
		scoped, err := emb.Client().DatabaseScoped("fwd-anchor")
		if err != nil {
			t.Fatal(err)
		}
		var got []byte
		if err := retry(func() (err error) { got, err = scoped.KV().Get(ctx, "mixed/k1"); return err }); err != nil || string(got) != "from-qrdbd" {
			t.Fatalf("the embedded replica read %q, %v; want the qrdbd node's write", got, err)
		}
	})

	t.Run("the embedded node forwards to a qrdbd replica as its configured caller", func(t *testing.T) {
		db, replica := c.singleReplicaDatabaseAvoiding(t, mixedEmbedded)
		scoped, err := emb.Client().DatabaseScoped(db)
		if err != nil {
			t.Fatal(err)
		}
		if err := retry(func() error { return scoped.KV().Put(ctx, "mixed/k2", []byte("from-embedded")) }); err != nil {
			t.Fatalf("the embedded node's forwarded Put to %s: %v", db, err)
		}
		if a := rest.get(t, c, replica, fwdAsWriter, db, "mixed/k2"); !a.ok || a.servedBy != "" || a.value != "from-embedded" {
			t.Fatalf("qrdbd replica %d answered %s; want the embedded node's write, served locally", replica+1, a.detail)
		}
	})
}

// fwdAnswerError is a transport answer that was not a success.
type fwdAnswerError struct{ a fwdAnswer }

func (e *fwdAnswerError) Error() string { return "answered " + e.a.detail }

// singleReplicaDatabaseAvoiding creates single-replica databases until one is
// placed on a node other than avoid, and returns it and that node.
func (c *fwdCluster) singleReplicaDatabaseAvoiding(t *testing.T, avoid int) (string, int) {
	t.Helper()
	for k := range 12 {
		name := "mixed-db-" + strconv.Itoa(k)
		p := c.placementOf(t, c.createDatabase(t, name, 1), 1)
		for i := range p.replicas {
			if i != avoid {
				return name, i
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("twelve single-replica databases were all placed on node %d", avoid+1)
	return "", 0
}

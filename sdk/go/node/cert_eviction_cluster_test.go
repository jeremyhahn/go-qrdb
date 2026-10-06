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
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// certSource narrows cert.CertProvider to the clienttrust.CertSource
// capability this test needs (CACertPEMs' multi-tenant enumeration), the
// same type assertion pkg/clienttrust.Pool itself performs.
type certSource interface {
	CACertPEMs(ctx context.Context) ([][]byte, error)
}

// TestClusteredNode_KeyBCacheEvictionPropagatesToEveryNode is go-qrdb#591's
// review, item 1's multi-node proof: a tenant's Key B CA material deletion
// -- replicated through Raft like any other write -- evicts EVERY node's
// own cached per-tenant cakey.SoftwareProvider independently, not only the
// node that performed the delete, and a subsequent certificate issuance on
// each node establishes a FRESH CA rather than continuing to serve the
// deleted one out of memory.
//
// This does NOT literally delete a tenant and recreate it under the SAME
// id through pkg/tenant's own DeleteTenant/CreateTenant. A tombstoned
// tenant id is retired PERMANENTLY by design
// (pkg/tenant/tombstone.go: "a deleted id is retired, not released" --
// CreateTenant refuses a tombstoned id forever, which is itself a
// deliberate security property -- a new principal must never silently
// read a previous principal's data under a reused id -- that this review
// must not defeat to make a test pass). Today's tenant teardown also does
// not itself delete __sys:cert_ca/{tenantID}/ records, so DeleteTenant
// alone has no externally observable effect on CACertPEMs' output here to
// test against (see pkg/cert/watch_eviction_test.go's tombstone-triggered
// tests for what DeleteTenant's tombstone write DOES prove: eviction and
// OnInvalidate firing, independent of whether anything else changed).
//
// What IS both achievable and faithful to the property under review --
// eviction, and the fresh-CA consequence of it, propagating to every
// replica, not just the one that issued the write -- is simulating the
// trigger WatchForDeletions actually reacts to: __sys:cert_ca/ material
// and certificate records disappearing. Deleted here through the node's
// own privileged KV service (n.Services().KV), which a real admin
// operation or a future reclamation step would use, never through the
// confined ordinary client.
func TestClusteredNode_KeyBCacheEvictionPropagatesToEveryNode(t *testing.T) {
	ca, replicas := newFormationReplicas(t, 3)
	// formationConfig itself leaves Seal disabled (it is tuned for pure
	// leader-election/replication-identity tests); this test needs
	// DefaultCertProvider actually constructed (lifecycle.BuildServices
	// only does so when a BarrierRegistry exists), so seal is turned on
	// here with the simplest strategy -- no ceremony, no explicit Unseal
	// call needed, exactly like sdk/go/conformance's own single-node
	// cells.
	nodes, err := bootFormationClusterWith(t, ca, replicas, func(self uint64) string {
		return peerIdentitySpecs(t, self, replicas)
	}, func(cfg *qrdbconfig.Config) {
		cfg.Seal.Enabled = true
		cfg.Seal.Strategy = "none"
	})
	if err != nil {
		t.Fatalf("bootFormationClusterWith: %v", err)
	}
	leader := awaitAgreedLeader(t, nodes)
	leaderNode := nodes[leader-1]

	ctx, cancel := context.WithTimeout(context.Background(), clusterFormationBudget)
	defer cancel()

	const tenantID = "acme"
	_, adminCSRPEM, err := certtest.NewCSR("acme-admin")
	if err != nil {
		t.Fatalf("certtest.NewCSR: %v", err)
	}
	if _, err := leaderNode.Client().Tenants().CreateTenant(ctx, &transport.CreateTenantRequest{
		ID:            tenantID,
		Name:          "Acme Corp",
		IsolationType: transport.TenantIsolationPrefix,
		AdminUsername: "admin",
		AdminCSRPEM:   string(adminCSRPEM),
	}); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := certtest.Generate(ctx, leaderNode.Services().CertProv, &certtest.Request{
		Username: "alice", TenantID: tenantID,
	}); err != nil {
		t.Fatalf("IssueClientCert (establish): %v", err)
	}

	// Force every node -- including followers, which never themselves
	// signed anything for this tenant -- to discover and cache its CA via
	// CACertPEMs, exactly as the dynamic trust pool's own resolution would
	// on a real handshake. This also confirms the material/certificate
	// records committed on the leader have actually replicated.
	originalCerts := make([][]byte, len(nodes))
	for i, n := range nodes {
		src, ok := n.Services().CertProv.(certSource)
		if !ok {
			t.Fatalf("replica %d: CertProv is %T, want one implementing CACertPEMs", replicas[i].id, n.Services().CertProv)
		}
		pollUntilNoErr(t, ctx, func() error {
			pems, perr := src.CACertPEMs(ctx)
			if perr != nil {
				return perr
			}
			if len(pems) != 1 {
				return errNotReplicatedYet
			}
			originalCerts[i] = pems[0]
			return nil
		})
	}

	// Delete acme's Key B material and certificate through the PRIVILEGED,
	// unconfined KV service -- a confined ordinary caller cannot reach
	// these keys at all (go-qrdb#591's review, B1) -- on the leader. This
	// is a real, replicated write: every follower's own state machine
	// applies it and feeds every follower's own watch.Hub independently.
	if err := leaderNode.Services().KV.Delete(ctx, "__sys:cert_ca/"+tenantID+"/material"); err != nil {
		t.Fatalf("Delete(material): %v", err)
	}
	if err := leaderNode.Services().KV.Delete(ctx, "__sys:cert_ca/"+tenantID+"/certificate"); err != nil {
		t.Fatalf("Delete(certificate): %v", err)
	}

	// Every node -- the leader AND every follower, none of which ran the
	// delete themselves except the leader -- must independently evict its
	// own cache and establish a FRESH CA distinct from the one it cached
	// before.
	for i, n := range nodes {
		src := n.Services().CertProv.(certSource)
		pollUntilNoErr(t, ctx, func() error {
			if _, perr := certtest.Generate(ctx, n.Services().CertProv, &certtest.Request{
				Username: "alice", TenantID: tenantID,
			}); perr != nil {
				return perr
			}
			pems, perr := src.CACertPEMs(ctx)
			if perr != nil {
				return perr
			}
			if len(pems) != 1 {
				return errNotReplicatedYet
			}
			if string(pems[0]) == string(originalCerts[i]) {
				return errStillStale
			}
			return nil
		})
	}
}

var errNotReplicatedYet = &pollRetryError{"replication has not caught up yet"}
var errStillStale = &pollRetryError{"still serving the evicted CA"}

type pollRetryError struct{ msg string }

func (e *pollRetryError) Error() string { return e.msg }

// pollUntilNoErr retries fn, ignoring its error and waiting
// clusterPollInterval between attempts, until it succeeds or ctx ends.
func pollUntilNoErr(t *testing.T, ctx context.Context, fn func() error) {
	t.Helper()
	for {
		if err := fn(); err == nil {
			return
		} else if ctx.Err() != nil {
			t.Fatalf("pollUntilNoErr: %v (ctx: %v)", err, ctx.Err())
		}
		select {
		case <-ctx.Done():
			t.Fatalf("pollUntilNoErr: context ended: %v", ctx.Err())
		case <-time.After(clusterPollInterval):
		}
	}
}

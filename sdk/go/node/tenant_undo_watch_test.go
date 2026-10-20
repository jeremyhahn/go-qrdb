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
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// awaitFeatureActive waits until every node judges feature active
// cluster-wide.
func awaitFeatureActive(t *testing.T, nodes []*Node, feature featuregate.Feature) {
	t.Helper()
	deadline := time.Now().Add(clusterFormationBudget)
	for i, n := range nodes {
		if n.featureGate == nil {
			t.Fatalf("node %d gates no features", i)
		}
		for !n.featureGate.Enabled(0, feature) {
			if time.Now().After(deadline) {
				t.Fatalf("node %d: %s not active within %s", i, feature, clusterFormationBudget)
			}
			time.Sleep(clusterPollInterval)
		}
	}
}

// Every member an sdk/go/node composer builds acknowledges an undo another
// member ran, while it serves: the undo runs on the leader, nothing restarts
// and no reconcile is requested, so the id is released only when both
// followers' undone-tenant watches have acknowledged it.
func TestClusteredNode_FollowersAcknowledgeAnUndoWhileServing(t *testing.T) {
	ca, replicas := newFormationReplicas(t, 3)
	nodes, err := bootFormationCluster(t, ca, replicas, func(self uint64) string {
		return peerIdentitySpecs(t, self, replicas)
	})
	if err != nil {
		t.Fatalf("NewClusteredNode: %v", err)
	}
	awaitFeatureActive(t, nodes, featuregate.TenantUndo)

	leaderID := awaitAgreedLeader(t, nodes)
	var leader *Node
	for i, r := range replicas {
		if r.id == leaderID {
			leader = nodes[i]
		}
	}
	if leader == nil {
		t.Fatalf("leader %d is not a booted replica", leaderID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), clusterFormationBudget)
	defer cancel()
	tenants := leader.Services().Tenant
	if _, err := tenants.CreateTenant(ctx, "acme", "Acme", tenant.IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	undoer, ok := tenants.(tenant.CreateUndoer)
	if !ok {
		t.Fatalf("tenant service %T cannot undo a create", tenants)
	}
	if err := undoer.UndoCreateTenant(ctx, "acme"); err != nil {
		t.Fatalf("UndoCreateTenant: %v", err)
	}

	// Shorter than ctx, so a missing acknowledgement reports the pending
	// nodes rather than a create cut off by the deadline.
	deadline := time.Now().Add(clusterFormationBudget / 2)
	for {
		_, err := tenants.CreateTenant(ctx, "acme", "Acme", tenant.IsolationPrefix)
		if err == nil {
			return
		}
		var pending *tenant.TenantUndoPendingError
		if !errors.As(err, &pending) {
			t.Fatalf("re-create of the undone id: %v, want success once every member acknowledged", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("id still awaits %v; the followers' watches never acknowledged", pending.Pending)
		}
		time.Sleep(clusterPollInterval)
	}
}

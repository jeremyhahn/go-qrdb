//go:build !integration

package node

import (
	"context"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
)

// A strict clustered node restarting while its peers are down must not
// block on the startup holder check: the check reads the user and role
// stores locally, as cmd/qrdbd's boot stores do, and with no conflicting
// holder passes. Two of three replicas stop, so the survivor's runtime
// services, which read linearizably, cannot answer; the boot services still
// do.
func TestBootIdentityServices_StrictHolderCheckNeedsNoQuorum(t *testing.T) {
	ca, replicas := newFormationReplicas(t, 3)
	nodes, err := bootFormationCluster(t, ca, replicas, func(self uint64) string {
		return peerIdentitySpecs(t, self, replicas)
	})
	if err != nil {
		t.Fatalf("NewClusteredNode: %v", err)
	}
	for i, n := range nodes[1:] {
		if err := n.Close(); err != nil {
			t.Fatalf("close replica %d: %v", replicas[i+1].id, err)
		}
	}
	survivor := nodes[0]
	svcNode := survivor.Services()
	if svcNode == nil {
		t.Fatal("survivor has no services")
	}

	// The runtime services cannot answer without a quorum: this is the
	// read the strict startup check used to block on.
	short, cancelShort := context.WithTimeout(context.Background(), 2*time.Second)
	linErr := firstboot.CheckStrictSeparationHolders(short, svcNode.User, svcNode.Role)
	cancelShort()
	if linErr == nil {
		t.Fatal("runtime holder check answered without a quorum; the survivor still has one")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	users, roles, err := survivor.bootIdentityServices(svcNode)
	if err != nil {
		t.Fatalf("bootIdentityServices: %v", err)
	}
	if err := firstboot.CheckStrictSeparationHolders(ctx, users, roles); err != nil {
		t.Fatalf("strict startup holder check without a quorum: %v", err)
	}
}

//go:build !integration

package node

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// A clustered node whose ceremony issued no system SO starts without a
// quorum: the startup hand-off reads the pending records locally, as
// cmd/qrdbd does, and with nothing issued proposes nothing. Two of three
// replicas stop, so the survivor cannot serve a linearizable read; the boot
// store still answers "absent" and the startup write completes.
func TestBootPendingRecordStore_NoSOIssuedNeedsNoQuorum(t *testing.T) {
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

	// The linearizable store cannot answer without a quorum: this is the
	// read startup used to block on.
	short, cancelShort := context.WithTimeout(context.Background(), 2*time.Second)
	_, linErr := PendingAdminCertStore(svcNode.KV).Get(short, firstboot.PendingSystemSOKey)
	cancelShort()
	var notFound interface{ KeyNotFound() string }
	if linErr == nil || errors.As(linErr, &notFound) {
		t.Fatalf("linearizable read answered without a quorum (err=%v); the survivor still has one", linErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store := survivor.bootPendingRecordStore(svcNode)
	_, err = store.Get(ctx, firstboot.PendingSystemSOKey)
	if !errors.As(err, &notFound) {
		t.Fatalf("boot store read without a quorum: got %v, want not-found", err)
	}
	// A ceremony that issued nothing: no SO, no admin certificate.
	core, err := ceremony.NewCeremonyService(&ceremony.CeremonyConfig{}, ceremonyHandoffBarrier(t), ceremonyHandoffLogger())
	if err != nil {
		t.Fatalf("ceremony: %v", err)
	}
	if err := firstboot.WriteIssuedSystemSOIfAny(ctx, core, store, service.LeadershipTransient); err != nil {
		t.Fatalf("startup system SO hand-off without a quorum: %v", err)
	}
	if err := firstboot.WriteIssuedAdminCertIfAny(ctx, core, store, service.LeadershipTransient); err != nil {
		t.Fatalf("startup admin hand-off without a quorum: %v", err)
	}
}

// TestClose_QuorumLessSurvivorsCloseClean closes every replica of a formed
// cluster in turn, leader first. The later closes run without a leader or a
// quorum, so the deregistration each proposes cannot commit; that best-effort
// cleanup must not fail Close.
func TestClose_QuorumLessSurvivorsCloseClean(t *testing.T) {
	ca, replicas := newFormationReplicas(t, 3)
	nodes, err := bootFormationCluster(t, ca, replicas, func(self uint64) string {
		return peerIdentitySpecs(t, self, replicas)
	})
	if err != nil {
		t.Fatalf("NewClusteredNode: %v", err)
	}
	// Close the leader first, so the followers' deregistrations go out
	// while the shard has no leader -- the shutdown that failed Close with
	// "not leader for shard, leader unknown".
	leader := awaitAgreedLeader(t, nodes)
	order := make([]int, 0, len(nodes))
	for i := range nodes {
		if replicas[i].id == leader {
			order = append([]int{i}, order...)
		} else {
			order = append(order, i)
		}
	}
	for _, i := range order {
		if err := nodes[i].Close(); err != nil {
			t.Fatalf("close replica %d: %v", replicas[i].id, err)
		}
	}
}

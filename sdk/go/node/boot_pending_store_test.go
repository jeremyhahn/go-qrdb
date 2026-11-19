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
	// Closing the replicas is how this test takes the survivor's quorum
	// away. A replica closed after the first may find the shard leaderless
	// (the first was its leader) and its deregistration refused: that is
	// the quorum loss this test makes, recorded rather than failed.
	for i, n := range nodes[1:] {
		err := n.Close()
		if err == nil {
			continue
		}
		if i > 0 && service.LeadershipTransient(err) {
			t.Logf("close replica %d without a leader: %v", replicas[i+1].id, err)
			continue
		}
		t.Fatalf("close replica %d: %v", replicas[i+1].id, err)
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

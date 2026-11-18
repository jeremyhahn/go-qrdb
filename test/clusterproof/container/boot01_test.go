//go:build clusterproof && clusterproof_container

package container

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/nemesis"
)

const (
	// bootTimeout bounds first boot and BOOT-01's checks together.
	bootTimeout = 5 * time.Minute
	// callTimeout bounds each SDK call.
	callTimeout = 10 * time.Second
	// logTail is how much of each container's log a failed run reports.
	logTail = 200
)

// TestBOOT01 is BOOT-01 against real containers: a static bootstrap of the
// inventory's founders from the release image, first boot on each, then
// health, status, peers and topology on every node over every transport,
// and capabilities reporting raft mode. PROOF_INVENTORY names the
// inventory; without one there is no cluster to prove, and that is a
// failure, not a skip.
func TestBOOT01(t *testing.T) {
	path := os.Getenv("PROOF_INVENTORY")
	if path == "" {
		t.Fatal("PROOF_INVENTORY must name a container inventory (docs/cluster-verification/user.md)")
	}
	inv, err := LoadInventory(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCluster(inv, nemesis.CommandRunner{})
	if err != nil {
		t.Fatal(err)
	}
	dial, err := NewDialer(inv.Credentials, callTimeout)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), bootTimeout)
	defer cancel()

	ids, err := c.Identities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range inv.Nodes {
		t.Logf("%s: replica %d at %s, identity %s", n.Name, n.ReplicaID, n.RaftAddress(), ids[n.ReplicaID])
	}
	// A container left by an earlier run would hold the name; the data
	// directory must be fresh regardless.
	if err := c.Remove(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if t.Failed() {
			for _, n := range inv.Nodes {
				logs, err := c.Logs(ctx, n, logTail)
				t.Logf("--- %s log (last %d lines, err %v) ---\n%s", n.Name, logTail, err, logs)
			}
		}
		if err := c.Remove(ctx); err != nil {
			t.Errorf("remove containers: %v", err)
		}
	})
	if err := c.Start(ctx, ids); err != nil {
		t.Fatal(err)
	}
	t.Log("started", len(inv.Nodes), "containers from", inv.Image)

	boot := &Boot{Inventory: inv, Dial: dial, Interval: time.Second}
	if err := boot.FirstBoot(ctx); err != nil {
		t.Fatal(err)
	}
	t.Log("first boot done on every node")
	transcript, err := boot.Verify(ctx)
	for _, o := range transcript {
		t.Log(o)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Log("BOOT-01 passed")
}

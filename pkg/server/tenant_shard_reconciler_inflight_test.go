package server

import (
	"context"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
)

// listingFactory is the factory as the reconciler sees it, except that each
// listing reports that it was taken, so the test can hold a start in flight
// until the reconciler has looked for it.
type listingFactory struct {
	*RaftBackendFactory
	listed chan<- struct{}
}

func (l listingFactory) ListOnHost() []StartedShard {
	defer func() { l.listed <- struct{}{} }()
	return l.RaftBackendFactory.ListOnHost()
}

func (l listingFactory) ListStarted() []StartedShard {
	defer func() { l.listed <- struct{}{} }()
	return l.RaftBackendFactory.ListStarted()
}

// TestTenantShardReconciler_DeleteDuringStartRetiresTheShard is the Codex
// round-10 finding on #1576: a placement deleted while its shard is still
// starting -- on the host, waiting to serve -- was looked up in ListStarted,
// which names serving shards only, so the delete found nothing to close and
// the shard went on to serve with its dedicated WAL held. The delete now finds
// the start through ListOnHost, waits for it, and closes the shard once the
// start completes.
func TestTenantShardReconciler_DeleteDuringStartRetiresTheShard(t *testing.T) {
	host, mux := newSharedTestHostWithMux(t)
	reg := newVolumePooledRegistry(t)
	src := newFakeRaftInfoSource()
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src,
		volTestLogger(), WithMultiplexLogDB(mux))
	t.Cleanup(func() { _ = f.CloseAll() })

	const shardID = uint64(2410)
	root, _ := registerVolumeTenant(t, reg, "rt")
	src.add("rt", "a", shardID, root)
	src.setDedicated("rt", "a")

	gated := make(chan struct{}, 1)
	release := make(chan struct{})
	f.servingOverride = func(ctx context.Context, h *quicraft.Host, id uint64) error {
		gated <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return WaitForShardServing(ctx, h, id)
	}

	started := resolveAsync(context.Background(), f, "rt", "a")
	select {
	case <-gated:
	case <-time.After(lockTestDeadline):
		t.Fatal("the start never reached its wait for the shard to serve")
	}
	if !mux.IsDedicated(shardID) {
		t.Fatal("precondition: the start in flight holds a dedicated WAL")
	}

	listed := make(chan struct{}, 4)
	r := NewTenantShardReconciler(nil, listingFactory{RaftBackendFactory: f, listed: listed}, sharedHostRaftAddr, nil)
	handled := make(chan struct{})
	go func() {
		defer close(handled)
		r.handleEvent(context.Background(), placement.Event{
			Type: placement.EventDelete, TenantID: "rt", DatabaseID: "a-id", ShardID: shardID,
		})
	}()
	// The delete looks the shard up while its start is still gated, so it
	// must find the start in flight rather than a shard already serving.
	select {
	case <-listed:
	case <-time.After(lockTestDeadline):
		t.Fatal("the delete never listed the shards on the host")
	}
	close(release)

	if res := <-started; res.err != nil {
		t.Fatalf("Services(a) = %v, want the start to complete", res.err)
	}
	select {
	case <-handled:
	case <-time.After(lockTestDeadline):
		t.Fatal("the delete never returned after the start completed")
	}
	if got := f.ListOnHost(); len(got) != 0 {
		t.Fatalf("ListOnHost = %v, want none: the deleted placement's shard was left running", got)
	}
	if _, err := host.ShardInfo(shardID); err == nil {
		t.Fatal("the deleted placement's shard still runs on the host")
	}
	if mux.IsDedicated(shardID) {
		t.Fatal("the deleted placement's shard still holds its dedicated WAL")
	}
}

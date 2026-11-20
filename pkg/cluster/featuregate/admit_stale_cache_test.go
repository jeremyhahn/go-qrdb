package featuregate

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/release"
)

// listFaultStore is an advertisement store without a watch whose listing
// fails once failing is set.
type listFaultStore struct {
	noWatchStore
	failing atomic.Bool
}

func (s *listFaultStore) ListMembers(ctx context.Context) ([]cluster.Member, error) {
	if s.failing.Load() {
		return nil, errors.New("advertisements unreadable")
	}
	return s.noWatchStore.ListMembers(ctx)
}

// TestAdmitReplica_ReadsAdvertisementsAuthoritatively: node 2 restarts from
// 0.59 onto 0.58 while this node's cached view still holds its 0.59
// advertisement (no watch, an hour's refresh). Admission must read the
// advertisements afresh and refuse the 0.60 candidate across the gap to
// node 2, which the restart did not signal through ConfigChangeID.
func TestAdmitReplica_ReadsAdvertisementsAuthoritatively(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.59.0-alpha"))
	ls, kv := newLeaseStore(), newMemKV()
	ls.advertiseRelease(t, 2, "n2:7000", "0.59.0-alpha")
	ls.advertiseRelease(t, 3, "n3:7000", "0.59.0-alpha")
	ls.advertiseRelease(t, 4, "n4:7000", "0.60.0-alpha")
	g := newGate(t, threeNodes())
	require.NoError(t, g.Start(context.Background(), kv, noWatchStore{ls}))
	require.NoError(t, g.AdmitReplica(systemShard, "n4:7000"), "0.60 is adjacent to every 0.59 member")

	// Node 2 restarts on 0.58: it records that release and re-advertises.
	_, err := kv.PutIfAbsent(context.Background(), ReleasePrefix+"2/0.58.0-alpha", []byte("n2:7000"))
	require.NoError(t, err)
	ls.advertiseRelease(t, 2, "n2:7000", "0.58.0-alpha")

	var gap *release.GapError
	require.ErrorAs(t, g.AdmitReplica(systemShard, "n4:7000"), &gap)
	assert.Equal(t, "0.58.0-alpha", gap.Local)
	assert.Equal(t, "0.60.0-alpha", gap.Peer)
}

// TestAdmitReplica_ReadsReleaseRecordsAuthoritatively: node 2's
// advertisement has lapsed and the release it recorded on its 0.58 restart
// is not in this node's cached records. Admission must read the records
// afresh and refuse the 0.60 candidate.
func TestAdmitReplica_ReadsReleaseRecordsAuthoritatively(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.59.0-alpha"))
	ls, kv := newLeaseStore(), newMemKV()
	ls.advertiseRelease(t, 2, "n2:7000", "0.59.0-alpha")
	ls.advertiseRelease(t, 3, "n3:7000", "0.59.0-alpha")
	ls.advertiseRelease(t, 4, "n4:7000", "0.60.0-alpha")
	_, err := kv.PutIfAbsent(context.Background(), ReleasePrefix+"2/0.59.0-alpha", []byte("n2:7000"))
	require.NoError(t, err)
	g := newGate(t, threeNodes())
	require.NoError(t, g.Start(context.Background(), kv, noWatchStore{ls}))

	_, err = kv.PutIfAbsent(context.Background(), ReleasePrefix+"2/0.58.0-alpha", []byte("n2:7000"))
	require.NoError(t, err)
	require.NoError(t, kv.Delete(context.Background(), ReleasePrefix+"2/0.59.0-alpha"))
	ls.expire(2)

	var gap *release.GapError
	require.ErrorAs(t, g.AdmitReplica(systemShard, "n4:7000"), &gap)
	assert.Equal(t, "0.58.0-alpha", gap.Local)
}

// TestAdmitReplica_UnreadableStoresRefuse: admission that cannot read the
// advertisements or the release records refuses rather than falling back
// to the cached view.
func TestAdmitReplica_UnreadableStoresRefuse(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.59.0-alpha"))
	ls := newLeaseStore()
	ls.advertiseRelease(t, 2, "n2:7000", "0.59.0-alpha")
	ls.advertiseRelease(t, 3, "n3:7000", "0.59.0-alpha")
	ls.advertiseRelease(t, 4, "n4:7000", "0.60.0-alpha")
	store := &listFaultStore{noWatchStore: noWatchStore{ls}}
	kv := &releaseUnreadableKV{memKV: newMemKV()}
	g := newGate(t, threeNodes())
	require.NoError(t, g.Start(context.Background(), kv, store))
	require.NoError(t, g.AdmitReplica(systemShard, "n4:7000"))

	var storeErr *StoreError
	store.failing.Store(true)
	require.ErrorAs(t, g.AdmitReplica(systemShard, "n4:7000"), &storeErr)
	assert.Equal(t, "list advertisements", storeErr.Op)

	store.failing.Store(false)
	kv.failing.Store(true)
	require.ErrorAs(t, g.AdmitReplica(systemShard, "n4:7000"), &storeErr)
	assert.Equal(t, "list releases", storeErr.Op)
}

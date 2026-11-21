package featuregate

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/release"
)

// setMembersErr makes every later GroupMembership read fail with err.
func setMembersErr(m *fakeMembers, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// adjacentCluster starts a gate on a 0.59 node whose members n2 and n3 run
// 0.59, whose candidate n4 advertises the adjacent 0.59.1 and whose
// candidate n5 advertises no release.
func adjacentCluster(t *testing.T, members *fakeMembers) *Gate {
	t.Helper()
	t.Cleanup(release.SetForTesting("0.59.0-alpha"))
	store, kv := newLeaseStore(), newMemKV()
	store.advertiseRelease(t, 2, "n2:7000", "0.59.0-alpha")
	store.advertiseRelease(t, 3, "n3:7000", "0.59.0-alpha")
	store.advertiseRelease(t, 4, "n4:7000", "0.59.1-alpha")
	store.advertiseRelease(t, 5, "n5:7000", "") // predates announcement
	g := newGate(t, members)
	require.NoError(t, g.Start(context.Background(), kv, store))
	return g
}

func TestStaleConfigurationError_NamesBothChangeIDs(t *testing.T) {
	err := &StaleConfigurationError{ShardID: 7, Admitted: 0, Current: 4}
	assert.EqualError(t, err,
		"feature gate: shard 7 configuration changed since admission (admitted at change 0, now 4)")
}

func TestCheckConfigBinding(t *testing.T) {
	members := threeNodes() // system shard at change 3
	g := newGate(t, members)
	sys := g.systemShard

	assert.NoError(t, g.CheckConfigBinding(sys, ConfigBinding{ConfigChangeID: 3, Required: true}))

	var stale *StaleConfigurationError
	require.ErrorAs(t, g.CheckConfigBinding(sys, ConfigBinding{ConfigChangeID: 2, Required: true}), &stale)
	assert.Equal(t, StaleConfigurationError{ShardID: sys, Admitted: 2, Current: 3}, *stale)

	// Change ID 0 is a configuration like any other: bound to it, a later
	// change refuses the add.
	require.ErrorAs(t, g.CheckConfigBinding(sys, ConfigBinding{ConfigChangeID: 0, Required: true}), &stale)
	assert.Equal(t, StaleConfigurationError{ShardID: sys, Admitted: 0, Current: 3}, *stale)

	// An unbound verdict or another shard passes without reading membership.
	readErr := errors.New("membership unavailable")
	setMembersErr(members, readErr)
	assert.NoError(t, g.CheckConfigBinding(sys, ConfigBinding{ConfigChangeID: 99}))
	assert.NoError(t, g.CheckConfigBinding(dedicatedShard, ConfigBinding{ConfigChangeID: 99, Required: true}))

	// A required binding on the system shard fails closed on a read error.
	assert.ErrorIs(t, g.CheckConfigBinding(sys, ConfigBinding{ConfigChangeID: 3, Required: true}), readErr)
}

func TestCheckConfigBinding_InitialConfiguration(t *testing.T) {
	members := threeNodes()
	members.set(systemShard, 0, map[uint64]string{1: "n1:7000", 2: "n2:7000", 3: "n3:7000"})
	g := newGate(t, members)

	assert.NoError(t, g.CheckConfigBinding(systemShard, ConfigBinding{ConfigChangeID: 0, Required: true}))

	var stale *StaleConfigurationError
	require.ErrorAs(t, g.CheckConfigBinding(systemShard, ConfigBinding{ConfigChangeID: 5, Required: true}), &stale)
	assert.Equal(t, StaleConfigurationError{ShardID: systemShard, Admitted: 5, Current: 0}, *stale)
}

func TestAdmitReplicaAt_BindsTheSystemShardConfiguration(t *testing.T) {
	members := threeNodes()
	g := adjacentCluster(t, members)

	b, err := g.AdmitReplicaAt(systemShard, "n4:7000")
	require.NoError(t, err)
	assert.Equal(t, ConfigBinding{ConfigChangeID: 3, Required: true}, b)
	assert.NoError(t, g.CheckConfigBinding(systemShard, b))

	// Another shard's change index is a different counter: unbound.
	b, err = g.AdmitReplicaAt(dedicatedShard, "n4:7000")
	require.NoError(t, err)
	assert.Equal(t, ConfigBinding{}, b)

	// No membership compared: the node itself, or an unannounced candidate.
	b, err = g.AdmitReplicaAt(systemShard, "n1:7000")
	require.NoError(t, err)
	assert.Equal(t, ConfigBinding{}, b)
	b, err = g.AdmitReplicaAt(systemShard, "n5:7000")
	require.NoError(t, err)
	assert.Equal(t, ConfigBinding{}, b)

	// A membership change after admission makes the binding stale.
	members.set(systemShard, 4, map[uint64]string{1: "n1:7000", 2: "n2:7000", 3: "n3:7000"})
	var stale *StaleConfigurationError
	require.ErrorAs(t, g.CheckConfigBinding(systemShard, ConfigBinding{ConfigChangeID: 3, Required: true}), &stale)
	assert.Equal(t, StaleConfigurationError{ShardID: systemShard, Admitted: 3, Current: 4}, *stale)
}

func TestAdmitReplicaAt_InitialConfigurationIsRequired(t *testing.T) {
	members := threeNodes()
	members.set(systemShard, 0, map[uint64]string{1: "n1:7000", 2: "n2:7000", 3: "n3:7000"})
	g := adjacentCluster(t, members)

	b, err := g.AdmitReplicaAt(systemShard, "n4:7000")
	require.NoError(t, err)
	assert.Equal(t, ConfigBinding{ConfigChangeID: 0, Required: true}, b)

	members.set(systemShard, 1, map[uint64]string{1: "n1:7000", 2: "n2:7000", 3: "n3:7000", 4: "n4:7000"})
	var stale *StaleConfigurationError
	require.ErrorAs(t, g.CheckConfigBinding(systemShard, b), &stale)
	assert.Equal(t, StaleConfigurationError{ShardID: systemShard, Admitted: 0, Current: 1}, *stale)
}

func TestAdmitReplicaAt_RefusalsCarryNoBinding(t *testing.T) {
	t.Run("self release gap", func(t *testing.T) {
		t.Cleanup(release.SetForTesting("0.58.0-alpha"))
		store, kv := newLeaseStore(), newMemKV()
		store.advertiseRelease(t, 4, "n4:7000", "0.60.0-alpha")
		g := newGate(t, threeNodes())
		require.NoError(t, g.Start(context.Background(), kv, store))
		b, err := g.AdmitReplicaAt(systemShard, "n4:7000")
		var gap *release.GapError
		require.ErrorAs(t, err, &gap)
		assert.Equal(t, ConfigBinding{}, b)
	})
	t.Run("member release gap", func(t *testing.T) {
		t.Cleanup(release.SetForTesting("0.59.0-alpha"))
		store, kv := newLeaseStore(), newMemKV()
		store.advertiseRelease(t, 2, "n2:7000", "0.58.0-alpha")
		store.advertiseRelease(t, 4, "n4:7000", "0.60.0-alpha")
		g := newGate(t, threeNodes())
		require.NoError(t, g.Start(context.Background(), kv, store))
		b, err := g.AdmitReplicaAt(systemShard, "n4:7000")
		var gap *release.GapError
		require.ErrorAs(t, err, &gap)
		assert.Equal(t, ConfigBinding{}, b)
	})
	t.Run("membership unreadable", func(t *testing.T) {
		members := threeNodes()
		g := adjacentCluster(t, members)
		readErr := errors.New("membership unavailable")
		setMembersErr(members, readErr)
		b, err := g.AdmitReplicaAt(systemShard, "n4:7000")
		require.ErrorIs(t, err, readErr)
		assert.Equal(t, ConfigBinding{}, b)
	})
}

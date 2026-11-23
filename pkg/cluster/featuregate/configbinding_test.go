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

// TestProposeBound_SerializesRequiredBindings: a Required system-shard
// binding's recheck and proposal run under the gate's lock, so a second
// ProposeBound rechecks only after the first's proposal returned, against
// the configuration it committed, and is refused as stale without
// proposing. An unbound or other-shard binding proposes with no check.
func TestProposeBound_SerializesRequiredBindings(t *testing.T) {
	members := threeNodes()
	nodes := map[uint64]string{1: "n1:7000", 2: "n2:7000", 3: "n3:7000"}
	members.set(systemShard, 0, nodes)
	g := newGate(t, members)
	reads := func() int {
		members.mu.Lock()
		defer members.mu.Unlock()
		return members.reads
	}
	bound := ConfigBinding{ConfigChangeID: 0, Required: true}

	entered, release1 := make(chan struct{}), make(chan struct{})
	readsWhileHeld := make(chan int, 1)
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- g.ProposeBound(systemShard, bound, func(<-chan struct{}) (<-chan struct{}, error) {
			close(entered)
			<-release1
			readsWhileHeld <- reads()
			// The add commits: the configuration moves on.
			members.set(systemShard, 1, nodes)
			return nil, nil
		})
	}()
	<-entered
	held := reads()

	secondProposed := make(chan struct{}, 1)
	secondStarted, secondDone := make(chan struct{}), make(chan error, 1)
	go func() {
		close(secondStarted)
		secondDone <- g.ProposeBound(systemShard, bound, func(<-chan struct{}) (<-chan struct{}, error) {
			secondProposed <- struct{}{}
			return nil, nil
		})
	}()
	<-secondStarted
	close(release1)

	require.NoError(t, <-firstDone)
	assert.Equal(t, held, <-readsWhileHeld, "the second ProposeBound rechecked while the first held the lock")
	var stale *StaleConfigurationError
	require.ErrorAs(t, <-secondDone, &stale)
	assert.Equal(t, StaleConfigurationError{ShardID: systemShard, Admitted: 0, Current: 1}, *stale)
	assert.Empty(t, secondProposed, "a stale binding must not propose")

	before := reads()
	for _, b := range []struct {
		shard   uint64
		binding ConfigBinding
	}{
		{systemShard, ConfigBinding{ConfigChangeID: 0}},
		{systemShard + 1, ConfigBinding{ConfigChangeID: 0, Required: true}},
	} {
		ran := false
		require.NoError(t, g.ProposeBound(b.shard, b.binding, func(<-chan struct{}) (<-chan struct{}, error) { ran = true; return nil, nil }))
		assert.True(t, ran, "shard %d binding %+v did not propose", b.shard, b.binding)
	}
	assert.Equal(t, before, reads(), "an unbound or other-shard binding must not be checked")
}

// An add whose wait timed out with its outcome unknown returns its error at
// once but keeps the proposal lock until its outcome is known: a second bound
// add neither rechecks nor proposes until then, and then rechecks against the
// configuration the first add committed. Closing the Gate releases a fence
// whose outcome never arrives.
func TestProposeBound_AmbiguousAddStaysFenced(t *testing.T) {
	members := threeNodes()
	nodes := map[uint64]string{1: "n1:7000", 2: "n2:7000", 3: "n3:7000"}
	members.set(systemShard, 0, nodes)
	g := newGate(t, members)
	reads := func() int {
		members.mu.Lock()
		defer members.mu.Unlock()
		return members.reads
	}
	bound := ConfigBinding{ConfigChangeID: 0, Required: true}
	ambiguous := errors.New("add timed out, outcome unknown")

	pending := make(chan struct{})
	err := g.ProposeBound(systemShard, bound, func(<-chan struct{}) (<-chan struct{}, error) {
		return pending, ambiguous
	})
	require.ErrorIs(t, err, ambiguous, "ProposeBound must return the ambiguous add's error at once")
	require.False(t, g.proposeMu.TryLock(), "an ambiguous add released the proposal lock before its outcome")
	held := reads()

	secondProposed := make(chan struct{}, 1)
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- g.ProposeBound(systemShard, bound, func(<-chan struct{}) (<-chan struct{}, error) {
			secondProposed <- struct{}{}
			return nil, nil
		})
	}()
	require.False(t, g.proposeMu.TryLock(), "the fence ended before the outcome")
	assert.Equal(t, held, reads(), "the second ProposeBound rechecked while the ambiguous add was fenced")

	// The ambiguous add commits late: the configuration moves on, then its
	// outcome is delivered.
	members.set(systemShard, 1, nodes)
	close(pending)

	var stale *StaleConfigurationError
	require.ErrorAs(t, <-secondDone, &stale)
	assert.Equal(t, StaleConfigurationError{ShardID: systemShard, Admitted: 0, Current: 1}, *stale)
	assert.Empty(t, secondProposed, "a bound add proposed against the membership before the ambiguous add")

	// An outcome that never arrives is released when the Gate closes.
	never := make(chan struct{})
	current := ConfigBinding{ConfigChangeID: 1, Required: true}
	require.ErrorIs(t, g.ProposeBound(systemShard, current, func(<-chan struct{}) (<-chan struct{}, error) {
		return never, ambiguous
	}), ambiguous)
	require.False(t, g.proposeMu.TryLock(), "an ambiguous add released the proposal lock before its outcome")
	require.NoError(t, g.Close())
	require.True(t, g.proposeMu.TryLock(), "Close did not release the ambiguous add's fence")
	g.proposeMu.Unlock()

	// After Close an ambiguous add is not fenced at all.
	require.ErrorIs(t, g.ProposeBound(systemShard, current, func(<-chan struct{}) (<-chan struct{}, error) {
		return never, ambiguous
	}), ambiguous)
	require.True(t, g.proposeMu.TryLock(), "a closed Gate fenced an ambiguous add")
	g.proposeMu.Unlock()
}

// ProposeBound passes a fenced proposal the Gate's stop, which closes when
// the Gate closes, and an unfenced one the closed Unfenced stop.
func TestProposeBound_PassesTheGateStop(t *testing.T) {
	members := threeNodes()
	members.set(systemShard, 0, map[uint64]string{1: "n1:7000", 2: "n2:7000", 3: "n3:7000"})
	g := newGate(t, members)
	ambiguous := errors.New("add timed out, outcome unknown")

	var stop <-chan struct{}
	require.ErrorIs(t, g.ProposeBound(systemShard, ConfigBinding{Required: true}, func(s <-chan struct{}) (<-chan struct{}, error) {
		stop = s
		return make(chan struct{}), ambiguous
	}), ambiguous)
	require.NotNil(t, stop)
	select {
	case <-stop:
		t.Fatal("a fenced proposal's stop was closed while the Gate is open")
	default:
	}
	require.NoError(t, g.Close())
	<-stop

	var unfenced <-chan struct{}
	require.NoError(t, g.ProposeBound(systemShard, ConfigBinding{}, func(s <-chan struct{}) (<-chan struct{}, error) {
		unfenced = s
		return nil, nil
	}))
	select {
	case <-unfenced:
	default:
		t.Fatal("an unfenced proposal's stop was open")
	}
}

func TestAdmitReplicaAt_BindsTheSystemShardConfiguration(t *testing.T) {
	members := threeNodes()
	g := adjacentCluster(t, members)

	b, err := g.AdmitReplicaAt(systemShard, "n4:7000")
	require.NoError(t, err)
	assert.Equal(t, uint64(3), b.ConfigChangeID)
	assert.True(t, b.Required)
	assert.Equal(t, "n4:7000", b.Candidate)
	assert.NotEmpty(t, b.ReleaseDigest)
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
	assert.Equal(t, uint64(0), b.ConfigChangeID)
	assert.True(t, b.Required)
	assert.NotEmpty(t, b.ReleaseDigest)

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

// A member restart changes its release without a configuration change, so
// the change ID alone cannot tell that the verdict no longer holds: a
// member rolled back to 0.58 after a 0.60 candidate was admitted against
// its 0.59 must make the binding stale, for the caller to admit again.
func TestCheckConfigBinding_MemberReleaseChangeAfterAdmissionIsStale(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.59.0-alpha"))
	store, kv := newLeaseStore(), newMemKV()
	store.advertiseRelease(t, 2, "n2:7000", "0.59.0-alpha")
	store.advertiseRelease(t, 3, "n3:7000", "0.59.0-alpha")
	store.advertiseRelease(t, 4, "n4:7000", "0.60.0-alpha")
	g := newGate(t, threeNodes())
	require.NoError(t, g.Start(context.Background(), kv, store))

	b, err := g.AdmitReplicaAt(systemShard, "n4:7000")
	require.NoError(t, err)
	require.True(t, b.Required)
	require.NoError(t, g.CheckConfigBinding(systemShard, b))

	store.advertiseRelease(t, 2, "n2:7000", "0.58.0-alpha")
	_, err = kv.PutIfAbsent(context.Background(), ReleasePrefix+"2/0.58.0-alpha", []byte("n2:7000"))
	require.NoError(t, err)

	var stale *StaleConfigurationError
	require.ErrorAs(t, g.CheckConfigBinding(systemShard, b), &stale)
	assert.True(t, stale.ReleasesChanged)
	assert.Equal(t, uint64(3), stale.Admitted)
	assert.Equal(t, uint64(3), stale.Current)

	// Admitting again sees the rollback and refuses the gap.
	var gap *release.GapError
	_, err = g.AdmitReplicaAt(systemShard, "n4:7000")
	require.ErrorAs(t, err, &gap)
	assert.Equal(t, "0.58.0-alpha", gap.Local)
}

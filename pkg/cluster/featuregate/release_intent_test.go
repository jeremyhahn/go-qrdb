package featuregate

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/release"
)

// intentBarrierKV holds each of the first two intent writes until both
// have been written, so two leaders' claims interleave write, write, list,
// list: the order in which a per-node lock alone admits both.
type intentBarrierKV struct {
	*memKV
	arrived atomic.Int32
	both    chan struct{}
}

func newIntentBarrierKV() *intentBarrierKV {
	return &intentBarrierKV{memKV: newMemKV(), both: make(chan struct{})}
}

func (k *intentBarrierKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	ok, err := k.memKV.PutIfAbsent(ctx, key, value)
	if strings.HasPrefix(key, ReleaseIntentPrefix) {
		switch k.arrived.Add(1) {
		case 1:
			select {
			case <-k.both:
			case <-time.After(10 * time.Second):
			}
		case 2:
			close(k.both)
		}
	}
	return ok, err
}

// intentsOf lists the intent keys kv holds for addr.
func intentsOf(t *testing.T, kv ActivationKV, addr string) []string {
	t.Helper()
	keys, err := kv.ListAll(context.Background(), ReleaseIntentPrefix+url.PathEscape(addr)+"/")
	require.NoError(t, err)
	return keys
}

// twoLeaders starts two gates -- system members 1 and 2 of an all-0.59
// membership -- over one system KV, with candidates n4 on 0.58 and n5 on
// 0.60 advertised, and returns each gate's binding for its candidate.
func twoLeaders(t *testing.T, kv ActivationKV) (a, b *Gate, bindA, bindB ConfigBinding) {
	t.Helper()
	t.Cleanup(release.SetForTesting("0.59.0-alpha"))
	store := newLeaseStore()
	for id, addr := range map[uint64]string{1: "n1:7000", 2: "n2:7000", 3: "n3:7000"} {
		store.advertiseRelease(t, id, addr, "0.59.0-alpha")
	}
	store.advertiseRelease(t, 4, "n4:7000", "0.58.0-alpha")
	store.advertiseRelease(t, 5, "n5:7000", "0.60.0-alpha")
	a = newGate(t, threeNodes())
	b, err := New(Config{Self: Self{ReplicaID: 2}, SystemShardID: systemShard, Members: threeNodes(), RefreshInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	require.NoError(t, a.Start(context.Background(), kv, store))
	require.NoError(t, b.Start(context.Background(), kv, store))
	bindA, err = a.AdmitReplicaAt(systemShard, "n4:7000")
	require.NoError(t, err, "0.58 is adjacent to every 0.59 member")
	bindB, err = b.AdmitReplicaAt(systemShard, "n5:7000")
	require.NoError(t, err, "0.60 is adjacent to every 0.59 member")
	require.Equal(t, []string{"0.58.0-alpha"}, bindA.CandidateReleases)
	return a, b, bindA, bindB
}

// Two leaders in turn each admit a candidate adjacent to every member but
// not to the other's (0.58 and 0.60 into all-0.59). Each holds only its own
// proposal lock, and go-quicraft compares no release, so the intent claim
// is what keeps both adds from being proposed: with both intents written
// before either is checked, at most one proposes and the other is refused
// with the gap, and a refused claim leaves no intent behind.
func TestProposeBound_ConcurrentLeadersNeverBothAdmitAGap(t *testing.T) {
	kv := newIntentBarrierKV()
	a, b, bindA, bindB := twoLeaders(t, kv)

	var proposed atomic.Int32
	propose := func(<-chan struct{}) (<-chan struct{}, error) {
		proposed.Add(1)
		return nil, nil
	}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = a.ProposeBound(systemShard, bindA, propose) }()
	go func() { defer wg.Done(); errs[1] = b.ProposeBound(systemShard, bindB, propose) }()
	wg.Wait()

	assert.LessOrEqual(t, proposed.Load(), int32(1), "both non-adjacent adds were proposed")
	refused := 0
	for i, err := range errs {
		if err == nil {
			continue
		}
		var gap *release.GapError
		require.ErrorAs(t, err, &gap)
		assert.Equal(t, "raft-membership", gap.Channel)
		refused++
		addr := []string{"n4:7000", "n5:7000"}[i]
		assert.Empty(t, intentsOf(t, kv, addr), "a refused claim left its intent")
	}
	assert.GreaterOrEqual(t, refused, 1)
}

// In turn, the first leader's add commits and keeps its intent; the second
// leader's non-adjacent candidate is refused on that intent and withdraws
// its own. Once the admitted member records its release, the intent is
// retired.
func TestProposeBound_SequentialLeadersRefuseOnALiveIntent(t *testing.T) {
	kv := newMemKV()
	a, b, bindA, bindB := twoLeaders(t, kv)

	require.NoError(t, a.ProposeBound(systemShard, bindA, func(<-chan struct{}) (<-chan struct{}, error) { return nil, nil }))
	assert.Equal(t, []string{ReleaseIntentPrefix + "n4:7000/0.58.0-alpha/1/3"}, intentsOf(t, kv, "n4:7000"))

	ran := false
	var gap *release.GapError
	require.ErrorAs(t, b.ProposeBound(systemShard, bindB, func(<-chan struct{}) (<-chan struct{}, error) { ran = true; return nil, nil }), &gap)
	assert.False(t, ran)
	assert.Equal(t, release.GapError{Channel: "raft-membership", Local: "0.58.0-alpha", Peer: "0.60.0-alpha"}, *gap)
	assert.Empty(t, intentsOf(t, kv, "n5:7000"))

	// n4 joins as replica 4 and records its release: the record carries the
	// claim from here on.
	joined := threeNodes()
	joined.set(systemShard, 4, map[uint64]string{1: "n1:7000", 2: "n2:7000", 3: "n3:7000", 4: "n4:7000"})
	t.Cleanup(release.SetForTesting("0.58.0-alpha"))
	n4, err := New(Config{Self: Self{ReplicaID: 4}, SystemShardID: systemShard, Members: joined, RefreshInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() { _ = n4.Close() })
	require.NoError(t, n4.EnforceRelease(context.Background(), kv))
	assert.Empty(t, intentsOf(t, kv, "n4:7000"), "the member's record did not retire its intent")
}

// An add that did not commit withdraws its intent: refused outright (nil
// pending, an error), or ambiguous and then resolved without the candidate
// becoming a member.
func TestProposeBound_UncommittedAddWithdrawsItsIntent(t *testing.T) {
	kv := newMemKV()
	a, _, bindA, _ := twoLeaders(t, kv)

	refusedAdd := assert.AnError
	require.ErrorIs(t, a.ProposeBound(systemShard, bindA, func(<-chan struct{}) (<-chan struct{}, error) { return nil, refusedAdd }), refusedAdd)
	assert.Empty(t, intentsOf(t, kv, "n4:7000"))

	pending := make(chan struct{})
	require.ErrorIs(t, a.ProposeBound(systemShard, bindA, func(<-chan struct{}) (<-chan struct{}, error) { return pending, refusedAdd }), refusedAdd)
	assert.NotEmpty(t, intentsOf(t, kv, "n4:7000"), "an ambiguous add withdrew its intent before its outcome")
	close(pending)
	eventually(t, func() bool { return len(intentsOf(t, kv, "n4:7000")) == 0 }, "a resolved, uncommitted add kept its intent")
}

// allOnRelease records rel for members 1, 2 and 3 of threeNodes.
func allOnRelease(t *testing.T, kv ActivationKV, rel string) {
	t.Helper()
	for id, addr := range map[uint64]string{1: "n1:7000", 2: "n2:7000", 3: "n3:7000"} {
		_, err := kv.PutIfAbsent(context.Background(), ReleasePrefix+strconv.FormatUint(id, 10)+"/"+rel, []byte(addr))
		require.NoError(t, err)
	}
}

// A member restarting onto 0.58 while a 0.60 candidate's add is claimed is
// refused on the intent, as it would be on the member, and its 0.58 record
// -- written before the check -- is withdrawn, the earlier one kept.
func TestEnforceRelease_RefusesARestartAcrossAClaimedCandidate(t *testing.T) {
	kv := newMemKV()
	allOnRelease(t, kv, "0.59.0-alpha")
	_, err := kv.PutIfAbsent(context.Background(), ReleaseIntentPrefix+"n5:7000/0.60.0-alpha/2/3", []byte("n5:7000"))
	require.NoError(t, err)
	t.Cleanup(release.SetForTesting("0.58.0-alpha"))
	g := newGate(t, threeNodes())

	var gap *release.GapError
	require.ErrorAs(t, g.EnforceRelease(context.Background(), kv), &gap)
	assert.Equal(t, release.GapError{Channel: "raft-membership", Local: "0.58.0-alpha", Peer: "0.60.0-alpha"}, *gap)
	require.ErrorAs(t, g.Refusal(), &gap, "the refusal is kept for health reporting")
	assert.False(t, kv.has(ReleasePrefix+"1/0.58.0-alpha"), "a refused restart left its record")
	assert.True(t, kv.has(ReleasePrefix+"1/0.59.0-alpha"), "a refused restart removed an earlier record")
}

// The reverse order: the 0.58 restart records first, while the leader's
// view still shows the member on 0.59, so the 0.60 candidate passes
// admission and its recheck; the claim then finds the 0.58 record and
// refuses the add.
func TestProposeBound_RefusesAnAddAcrossARecordedRestart(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.59.0-alpha"))
	kv, store := newMemKV(), newLeaseStore()
	allOnRelease(t, kv, "0.59.0-alpha")
	for id, addr := range map[uint64]string{1: "n1:7000", 2: "n2:7000", 3: "n3:7000"} {
		store.advertiseRelease(t, id, addr, "0.59.0-alpha")
	}
	store.advertiseRelease(t, 5, "n5:7000", "0.60.0-alpha")
	leader, err := New(Config{Self: Self{ReplicaID: 2}, SystemShardID: systemShard, Members: threeNodes(), RefreshInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() { _ = leader.Close() })
	require.NoError(t, leader.Start(context.Background(), kv, store))
	binding, err := leader.AdmitReplicaAt(systemShard, "n5:7000")
	require.NoError(t, err)

	restore := release.SetForTesting("0.58.0-alpha")
	require.NoError(t, newGate(t, threeNodes()).EnforceRelease(context.Background(), kv), "no intent is claimed yet")
	restore()
	require.True(t, kv.has(ReleasePrefix+"1/0.58.0-alpha"))

	ran := false
	var gap *release.GapError
	require.ErrorAs(t, leader.ProposeBound(systemShard, binding, func(<-chan struct{}) (<-chan struct{}, error) { ran = true; return nil, nil }), &gap)
	assert.False(t, ran, "the add was proposed across the recorded restart")
	assert.Equal(t, release.GapError{Channel: "raft-membership", Local: "0.58.0-alpha", Peer: "0.60.0-alpha"}, *gap)
	assert.Empty(t, intentsOf(t, kv, "n5:7000"))
}

// TestRecordRelease_KeepsAnUnresolvedAddsIntentAfterItsClaimantLapses covers
// an intent whose claimant's advertisement is gone while its add, bound to
// the current configuration, can still commit: the intent stays and keeps
// refusing a restart across it.
func TestRecordRelease_KeepsAnUnresolvedAddsIntentAfterItsClaimantLapses(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.59.0-alpha"))
	kv, store := newMemKV(), newLeaseStore()
	allOnRelease(t, kv, "0.59.0-alpha")
	for id, addr := range map[uint64]string{1: "n1:7000", 2: "n2:7000", 3: "n3:7000"} {
		store.advertiseRelease(t, id, addr, "0.59.0-alpha")
	}
	pending := ReleaseIntentPrefix + "n4:7000/0.58.0-alpha/9/3"
	_, err := kv.PutIfAbsent(context.Background(), pending, []byte("n4:7000"))
	require.NoError(t, err)

	member, err := New(Config{Self: Self{ReplicaID: 2}, SystemShardID: systemShard, Members: threeNodes(), RefreshInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() { _ = member.Close() })
	require.NoError(t, member.Start(context.Background(), kv, store))
	require.NoError(t, member.recordRelease(context.Background(), kv))
	assert.True(t, kv.has(pending), "the intent of an add that can still commit was retired")

	t.Cleanup(release.SetForTesting("0.60.0-alpha"))
	var gap *release.GapError
	require.ErrorAs(t, newGate(t, threeNodes()).EnforceRelease(context.Background(), kv), &gap)
	assert.Equal(t, release.GapError{Channel: "raft-membership", Local: "0.60.0-alpha", Peer: "0.58.0-alpha"}, *gap)
}

// TestRecordRelease_KeepsAnotherClaimantsIntentAfterItsConfigurationMoved
// covers an intent another node claimed whose bound configuration is past.
func TestRecordRelease_KeepsAnotherClaimantsIntentAfterItsConfigurationMoved(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.59.0-alpha"))
	kv := newMemKV()
	allOnRelease(t, kv, "0.59.0-alpha")
	orphan := ReleaseIntentPrefix + "n4:7000/0.58.0-alpha/9/2"
	_, err := kv.PutIfAbsent(context.Background(), orphan, []byte("n4:7000"))
	require.NoError(t, err)
	system, err := threeNodes().GroupMembership(systemShard)
	require.NoError(t, err)
	require.Greater(t, system.ConfigChangeID, uint64(2))

	require.NoError(t, newGate(t, threeNodes()).recordRelease(context.Background(), kv))
	assert.True(t, kv.has(orphan), "another claimant's intent was retired because the configuration moved")

	t.Cleanup(release.SetForTesting("0.60.0-alpha"))
	var gap *release.GapError
	require.ErrorAs(t, newGate(t, threeNodes()).EnforceRelease(context.Background(), kv), &gap)
}

// TestRecordRelease_KeepsAZeroBoundIntentOfAnotherClaimant covers an intent
// bound to configuration change 0, which Raft never compares: only its
// claimant or its candidate's record retires it.
func TestRecordRelease_KeepsAZeroBoundIntentOfAnotherClaimant(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.59.0-alpha"))
	kv := newMemKV()
	allOnRelease(t, kv, "0.59.0-alpha")
	unbound := ReleaseIntentPrefix + "n4:7000/0.58.0-alpha/9/0"
	_, err := kv.PutIfAbsent(context.Background(), unbound, []byte("n4:7000"))
	require.NoError(t, err)
	require.NoError(t, newGate(t, threeNodes()).recordRelease(context.Background(), kv))
	assert.True(t, kv.has(unbound), "another claimant's intent bound to 0 was retired")
}

// TestRecordRelease_RetiresOwnIntentWhenNoAddIsInFlight covers an intent
// this node claimed in an earlier process (or failed to withdraw): with
// proposeMu free no add of this node sits between claim and outcome, so a
// record pass, the one EnforceRelease runs at boot included, retires it.
// Another claimant's intent is kept: an unstarted gate cannot tell whether
// that claimant is live.
func TestRecordRelease_RetiresOwnIntentWhenNoAddIsInFlight(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.59.0-alpha"))
	kv := newMemKV()
	allOnRelease(t, kv, "0.59.0-alpha")
	own := ReleaseIntentPrefix + "n4:7000/0.58.0-alpha/1/3"
	other := ReleaseIntentPrefix + "n5:7000/0.60.0-alpha/2/3"
	for key, addr := range map[string]string{own: "n4:7000", other: "n5:7000"} {
		_, err := kv.PutIfAbsent(context.Background(), key, []byte(addr))
		require.NoError(t, err)
	}
	g := newGate(t, threeNodes())
	require.NoError(t, g.recordRelease(context.Background(), kv))
	assert.False(t, kv.has(own), "this node's idle intent was kept")
	assert.True(t, kv.has(other), "another claimant's intent bound to the current configuration was retired")
}

// TestRecordRelease_KeepsAnIntentWhileItsAddIsInFlight: while the add's
// outcome is pending, neither the claimant's record pass (proposeMu held by
// the fence) nor another member's (the claimant is advertised) retires the
// intent; an outcome without a commit withdraws it.
func TestRecordRelease_KeepsAnIntentWhileItsAddIsInFlight(t *testing.T) {
	kv := newMemKV()
	a, b, bindA, _ := twoLeaders(t, kv)

	pending := make(chan struct{})
	require.NoError(t, a.ProposeBound(systemShard, bindA, func(<-chan struct{}) (<-chan struct{}, error) { return pending, nil }))
	intent := []string{ReleaseIntentPrefix + "n4:7000/0.58.0-alpha/1/3"}
	require.Equal(t, intent, intentsOf(t, kv, "n4:7000"))

	require.NoError(t, a.recordRelease(context.Background(), kv))
	assert.Equal(t, intent, intentsOf(t, kv, "n4:7000"), "the claimant retired the intent of its in-flight add")
	require.NoError(t, b.recordRelease(context.Background(), kv))
	assert.Equal(t, intent, intentsOf(t, kv, "n4:7000"), "a member retired the intent of a live claimant")

	close(pending)
	eventually(t, func() bool { return len(intentsOf(t, kv, "n4:7000")) == 0 }, "a resolved, uncommitted add kept its intent")
}

package featuregate

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/release"
)

// prefixFaultKV fails ListAll and ListStale for one exact prefix, PutIfAbsent and Delete for
// keys under a prefix, and otherwise behaves as memKV.
type prefixFaultKV struct {
	*memKV
	mu       sync.Mutex
	listFail string
	putFail  string
	delFail  string
}

var errPrefixFault = errors.New("store unavailable")

func (k *prefixFaultKV) set(list, put, del string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.listFail, k.putFail, k.delFail = list, put, del
}

func (k *prefixFaultKV) ListAll(ctx context.Context, prefix string) ([]string, error) {
	k.mu.Lock()
	fail := k.listFail != "" && prefix == k.listFail
	k.mu.Unlock()
	if fail {
		return nil, errPrefixFault
	}
	return k.memKV.ListAll(ctx, prefix)
}

func (k *prefixFaultKV) ListStale(ctx context.Context, prefix string) ([]string, error) {
	k.mu.Lock()
	fail := k.listFail != "" && prefix == k.listFail
	k.mu.Unlock()
	if fail {
		return nil, errPrefixFault
	}
	return k.memKV.ListStale(ctx, prefix)
}

func (k *prefixFaultKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	k.mu.Lock()
	fail := k.putFail != "" && strings.HasPrefix(key, k.putFail)
	k.mu.Unlock()
	if fail {
		return false, errPrefixFault
	}
	return k.memKV.PutIfAbsent(ctx, key, value)
}

func (k *prefixFaultKV) Delete(ctx context.Context, key string) error {
	k.mu.Lock()
	fail := k.delFail != "" && strings.HasPrefix(key, k.delFail)
	k.mu.Unlock()
	if fail {
		return errPrefixFault
	}
	return k.memKV.Delete(ctx, key)
}

func newPrefixFaultKV() *prefixFaultKV { return &prefixFaultKV{memKV: newMemKV()} }

// gateAsReplica builds a gate whose own replica ID is id.
func gateAsReplica(t *testing.T, members MembershipSource, id uint64) *Gate {
	t.Helper()
	g, err := New(Config{
		Self:            Self{ReplicaID: id, Address: "n9:7000"},
		SystemShardID:   systemShard,
		Members:         members,
		RefreshInterval: time.Hour,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = g.Close() })
	return g
}

func TestReleaseErrors_Messages(t *testing.T) {
	var unannounced *UnannouncedCandidateError
	err := error(&UnannouncedCandidateError{ShardID: systemShard, Address: "n7:7000"})
	require.ErrorAs(t, err, &unannounced)
	assert.Contains(t, err.Error(), "n7:7000")
	assert.Contains(t, err.Error(), "enroll the node first")

	changed := &StaleConfigurationError{ShardID: 4, Admitted: 3, Current: 3, ReleasesChanged: true}
	assert.Contains(t, changed.Error(), "shard 4 member releases changed")
	assert.Contains(t, changed.Error(), "change 3")

	moved := &StaleConfigurationError{ShardID: 4, Admitted: 3, Current: 5}
	assert.Contains(t, moved.Error(), "admitted at change 3, now 5")
}

func TestReleaseParseIntents_SkipsMalformedKeys(t *testing.T) {
	valid := ReleaseIntentPrefix + "n1:7000/0.60.0-alpha/2/5"
	got := parseIntents([]string{
		valid,
		"other/n1:7000/0.60.0-alpha/2/5",
		ReleaseIntentPrefix + "%zz/0.60.0-alpha/2/5",
		ReleaseIntentPrefix + "n1:7000",
		ReleaseIntentPrefix + "/0.60.0-alpha/2/5",
		ReleaseIntentPrefix + "n1:7000/x",
		ReleaseIntentPrefix + "n1:7000/0.60.0-alpha/2/bad",
		ReleaseIntentPrefix + "n1:7000/0.60.0-alpha/5",
		ReleaseIntentPrefix + "n1:7000/0.60.0-alpha/x/5",
		ReleaseIntentPrefix + "n1:7000/0.60.0-alpha/0/5",
	})
	require.Len(t, got, 1)
	assert.Equal(t, releaseIntent{key: valid, addr: "n1:7000", rel: "0.60.0-alpha", claimant: 2, bound: 5}, got[0])
}

func TestReleaseIsSystemMember_Cases(t *testing.T) {
	members := threeNodes()
	g := newGate(t, members)
	assert.True(t, g.isSystemMember("n2:7000"))
	assert.False(t, g.isSystemMember("n9:7000"))
	setMembersErr(members, errors.New("membership unavailable"))
	assert.True(t, g.isSystemMember("n9:7000"), "an unreadable membership counts as a member")
}

func TestReleaseCheckRestartRelease_Failures(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.60.0-alpha"))
	ctx := context.Background()

	t.Run("not a member", func(t *testing.T) {
		g := gateAsReplica(t, threeNodes(), 9)
		assert.NoError(t, g.checkRestartRelease(ctx, newMemKV()))
	})
	t.Run("membership unreadable", func(t *testing.T) {
		members := threeNodes()
		g := newGate(t, members)
		boom := errors.New("membership unavailable")
		setMembersErr(members, boom)
		assert.ErrorIs(t, g.checkRestartRelease(ctx, newMemKV()), boom)
	})
	t.Run("releases unreadable", func(t *testing.T) {
		g := newGate(t, threeNodes())
		kv := newPrefixFaultKV()
		kv.set(ReleasePrefix, "", "")
		var storeErr *StoreError
		require.ErrorAs(t, g.checkRestartRelease(ctx, kv), &storeErr)
		assert.ErrorIs(t, storeErr, errPrefixFault)
	})
	t.Run("intents unreadable", func(t *testing.T) {
		g := newGate(t, threeNodes())
		kv := newPrefixFaultKV()
		kv.set(ReleaseIntentPrefix, "", "")
		var storeErr *StoreError
		require.ErrorAs(t, g.checkRestartRelease(ctx, kv), &storeErr)
		assert.Equal(t, "list release intents", storeErr.Op)
	})
}

func TestReleaseRecordRelease_Failures(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.60.0-alpha"))
	ctx := context.Background()

	t.Run("membership unreadable", func(t *testing.T) {
		members := threeNodes()
		g := newGate(t, members)
		boom := errors.New("membership unavailable")
		setMembersErr(members, boom)
		assert.ErrorIs(t, g.recordRelease(ctx, newMemKV()), boom)
	})
	t.Run("releases unreadable", func(t *testing.T) {
		g := newGate(t, threeNodes())
		kv := newPrefixFaultKV()
		kv.set(ReleasePrefix, "", "")
		assert.ErrorIs(t, g.recordRelease(ctx, kv), errPrefixFault)
	})
	t.Run("record write fails", func(t *testing.T) {
		g := newGate(t, threeNodes())
		kv := newPrefixFaultKV()
		kv.set("", ReleasePrefix, "")
		var storeErr *StoreError
		require.ErrorAs(t, g.recordRelease(ctx, kv), &storeErr)
		assert.Equal(t, "record release", storeErr.Op)
	})
	t.Run("joiner delete failure is not fatal", func(t *testing.T) {
		g := newGate(t, threeNodes())
		kv := newPrefixFaultKV()
		joiner := AdvertisementPrefix + "1"
		_, err := kv.PutIfAbsent(ctx, joiner, []byte(`{"node_id":1}`))
		require.NoError(t, err)
		kv.set("", "", AdvertisementPrefix)
		g.unleased.Store(true)
		require.NoError(t, g.recordRelease(ctx, kv))
		assert.True(t, kv.has(joiner))
		assert.True(t, kv.has(ReleasePrefix+"1/0.60.0-alpha"))
	})
}

func TestReleaseStartAdmission_Failures(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.60.0-alpha"))
	ctx := context.Background()

	t.Run("nil store", func(t *testing.T) {
		g := newGate(t, threeNodes())
		var cfg *ConfigError
		require.ErrorAs(t, g.StartAdmission(ctx, nil, newLeaseStore()), &cfg)
		require.ErrorAs(t, g.StartAdmission(ctx, newMemKV()), &cfg)
		require.ErrorAs(t, g.StartAdmission(ctx, newMemKV(), nil), &cfg)
		assert.Equal(t, "store", cfg.Field)
	})
	t.Run("activations unreadable", func(t *testing.T) {
		g := newGate(t, threeNodes())
		kv := newMemKV()
		kv.fail(errPrefixFault, errPrefixFault, nil)
		assert.ErrorIs(t, g.StartAdmission(ctx, kv, newLeaseStore()), errPrefixFault)
		assert.False(t, g.unleased.Load())
	})
	t.Run("release not recorded", func(t *testing.T) {
		g := newGate(t, threeNodes())
		kv := newPrefixFaultKV()
		kv.set(ReleasePrefix, "", "")
		assert.ErrorIs(t, g.StartAdmission(ctx, kv, newLeaseStore()), errPrefixFault)
		assert.False(t, g.unleased.Load(), "a failed admission start leaves the gate leased")
		kv.set("", "", "")
		require.NoError(t, g.StartAdmission(ctx, kv, newLeaseStore()))
	})
}

func TestReleaseCheckConfigBinding_UnreadableStores(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.60.0-alpha"))
	members := threeNodes()
	sys, err := members.GroupMembership(systemShard)
	require.NoError(t, err)
	b := ConfigBinding{Required: true, ConfigChangeID: sys.ConfigChangeID, ReleaseDigest: "digest", Candidate: "n4:7000"}

	t.Run("advertisements unreadable", func(t *testing.T) {
		g := newGate(t, members)
		g.stores.Store(&admissionStores{kv: newMemKV(), adverts: []cluster.MembershipStore{failingListStore{newLeaseStore()}}})
		var storeErr *StoreError
		require.ErrorAs(t, g.CheckConfigBinding(systemShard, b), &storeErr)
		assert.Equal(t, "list advertisements", storeErr.Op)
	})
	t.Run("releases unreadable", func(t *testing.T) {
		g := newGate(t, members)
		kv := newPrefixFaultKV()
		kv.set(ReleasePrefix, "", "")
		g.stores.Store(&admissionStores{kv: kv, adverts: []cluster.MembershipStore{newLeaseStore()}})
		assert.ErrorIs(t, g.CheckConfigBinding(systemShard, b), errPrefixFault)
	})
}

func TestReleaseAdmitReplicaAt_UnleasedChecksCandidateFeatures(t *testing.T) {
	t.Cleanup(release.SetForTesting("0.59.0-alpha"))
	ctx := context.Background()
	kv, store := newMemKV(), newLeaseStore()
	allOnRelease(t, kv, "0.59.0-alpha")
	_, err := kv.PutIfAbsent(ctx, ActivationPrefix+string(TenantUndo), []byte(`{}`))
	require.NoError(t, err)
	g := newGate(t, threeNodes())
	require.NoError(t, g.StartAdmission(ctx, kv, store))
	require.True(t, g.Activated(TenantUndo))

	store.advertiseRelease(t, 5, "n5:7000", "0.59.0-alpha")
	_, err = g.AdmitReplicaAt(systemShard, "n5:7000")
	require.NoError(t, err)

	a, err := cluster.NewAdvertiser(store, cluster.Member{
		NodeID: 6, Address: "n6:7000", ReleaseVersion: "0.59.0-alpha",
	}, cluster.AdvertiseConfig{})
	require.NoError(t, err)
	require.NoError(t, a.Register(ctx))
	_, err = g.AdmitReplicaAt(systemShard, "n6:7000")
	var unsupported *UnsupportedReplicaError
	require.ErrorAs(t, err, &unsupported)
	assert.Equal(t, "n6:7000", unsupported.Address)
}

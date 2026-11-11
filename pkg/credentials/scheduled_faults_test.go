package credentials

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// scheduledOpts claims "k" for a scheduled rotation and returns the options
// that commit it with value.
func scheduledOpts(t *testing.T, svc *CredentialService, value string) RotateOptions {
	t.Helper()
	claim, _, err := svc.claimRotation(t.Context(), "k", rotationEpoch.Add(time.Hour))
	require.NoError(t, err)
	require.NotNil(t, claim)
	return RotateOptions{
		NewValue:       []byte(value),
		retainPrevious: true,
		schedule:       &scheduledRotation{claim: claim, now: rotationEpoch},
	}
}

// requireUnrotated asserts "k" still holds before at version 1.
func requireUnrotated(t *testing.T, svc *CredentialService, before []byte) credentialMeta {
	t.Helper()
	ctx := context.Background()
	current, _, err := svc.loadBarrierCurrent(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, string(before), string(current), "the current value is kept")
	meta, err := svc.loadBarrierMeta(ctx, "k")
	require.NoError(t, err)
	assert.LessOrEqual(t, meta.CurrentVersion, uint64(1), "no version is committed")
	return *meta
}

// A scheduled rotation that cannot stage its value fails with the cause and
// stages nothing, so the next attempt asks the source again.
func TestRotateBarrier_ScheduledStageFaults(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fault func(*faultBackend)
		cause error
		op    string
	}{
		{name: "current read fails", fault: func(b *faultBackend) { arm(&b.failGet, credentialPrefix+"k") }, cause: errInjectedFault},
		{
			name: "stage contended",
			fault: func(b *faultBackend) {
				b.casLosses.Store(int32(rotationMetaRetries))
				arm(&b.loseCAS, metaKey("k"))
			},
			cause: ErrRotationMetaContended,
			op:    "stage rotation",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, backend := newFaultRotationService(t)
			setFaultPolicy(t, svc, "k", 3)
			before, _, err := svc.loadBarrierCurrent(t.Context(), "k")
			require.NoError(t, err)
			opts := scheduledOpts(t, svc, "scheduled")
			tc.fault(backend)

			_, err = svc.RotateCredential(t.Context(), "k", opts)
			require.ErrorIs(t, err, tc.cause)
			if tc.op != "" {
				requireOperationError(t, err, tc.op)
			}
			disarmAll(backend)
			meta := requireUnrotated(t, svc, before)
			assert.Zero(t, meta.Staged, "nothing is staged")
			assert.Equal(t, uint64(0), opts.schedule.staged)
		})
	}
}

// A scheduled rotation whose value is staged but whose commit cannot read
// the current value fails with the cause and keeps the staged value, so a
// resumed rotation commits it without asking the source again.
func TestRotateBarrier_ScheduledCommitCurrentReadFails(t *testing.T) {
	svc, backend := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 3)
	before, _, err := svc.loadBarrierCurrent(t.Context(), "k")
	require.NoError(t, err)
	opts := scheduledOpts(t, svc, "scheduled")
	staged := false
	backend.onPut = func(key string) {
		if staged || key != metaKey("k") {
			return
		}
		staged = true
		arm(&backend.failGet, credentialPrefix+"k")
	}

	_, err = svc.RotateCredential(t.Context(), "k", opts)
	require.ErrorIs(t, err, errInjectedFault)
	require.True(t, staged)
	disarmAll(backend)
	meta := requireUnrotated(t, svc, before)
	require.NotZero(t, meta.Staged, "the staged value is kept for the resumed rotation")
	value, err := svc.store.Get(t.Context(), stagedKey("k", meta.Staged))
	require.NoError(t, err)
	assert.Equal(t, "scheduled", string(value))
}

// A value stored between a scheduled rotation's stage and its commit takes
// the staged version's place: the commit is refused as a lost claim, the
// stored value stays current and the staged value is gone, whether or not
// the rotation's own delete of it succeeds.
func TestRotateBarrier_ScheduledCommitAfterStoreAbandons(t *testing.T) {
	for _, failDelete := range []bool{false, true} {
		t.Run(map[bool]string{false: "delete succeeds", true: "delete fails"}[failDelete], func(t *testing.T) {
			svc, backend := newFaultRotationService(t)
			setFaultPolicy(t, svc, "k", 3)
			opts := scheduledOpts(t, svc, "scheduled")
			stored := false
			backend.onPut = func(key string) {
				if stored || key != metaKey("k") {
					return
				}
				stored = true
				ctx := context.Background()
				if failDelete {
					meta, err := svc.loadBarrierMeta(ctx, "k")
					require.NoError(t, err)
					arm(&backend.failDelete, stagedKey("k", meta.Staged))
				}
				require.NoError(t, svc.storeBarrier(ctx, "k", []byte("manual")))
			}

			_, err := svc.RotateCredential(t.Context(), "k", opts)
			require.ErrorIs(t, err, ErrRotationClaimLost)
			require.True(t, stored)
			disarmAll(backend)
			meta := requireUnrotated(t, svc, []byte("manual"))
			assert.Zero(t, meta.Staged)
			_, err = backend.MemoryBackend.Get(t.Context(), stagedKey("k", opts.schedule.staged))
			assert.ErrorIs(t, err, seal.ErrSecretNotFound, "the staged value is gone")
		})
	}
}

// A scheduled rotation whose claim is replaced after it staged its value
// is refused as a lost claim and leaves the staged value, which the
// metadata still names, for the claim's new holder.
func TestRotateBarrier_ScheduledCommitAfterClaimReplacedKeepsNamedStage(t *testing.T) {
	svc, backend := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 3)
	before, _, err := svc.loadBarrierCurrent(t.Context(), "k")
	require.NoError(t, err)
	opts := scheduledOpts(t, svc, "scheduled")
	replaced := false
	backend.onPut = func(key string) {
		if replaced || key != metaKey("k") {
			return
		}
		replaced = true
		require.NoError(t, svc.updateBarrierMeta(context.Background(), "k", "replace claim", func(m *credentialMeta) bool {
			m.Claim = &rotationClaim{ID: "other", Until: m.Claim.Until}
			return true
		}))
	}

	_, err = svc.RotateCredential(t.Context(), "k", opts)
	require.ErrorIs(t, err, ErrRotationClaimLost)
	require.True(t, replaced)
	backend.onPut = nil
	meta := requireUnrotated(t, svc, before)
	require.NotNil(t, meta.Claim)
	assert.Equal(t, "other", meta.Claim.ID)
	assert.Equal(t, opts.schedule.staged, meta.Staged, "the metadata still names the staged version")
	value, err := svc.store.Get(t.Context(), stagedKey("k", meta.Staged))
	require.NoError(t, err)
	assert.Equal(t, "scheduled", string(value), "a staged value the metadata names is kept")
}

// A barrier store whose write fails, or whose metadata keeps changing under
// it, reports that and leaves the stored value in place.
func TestStoreBarrier_WriteFaults(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fault func(*faultBackend)
		cause error
	}{
		{name: "write fails", fault: func(b *faultBackend) { arm(&b.failPut, credentialPrefix+"k") }, cause: errInjectedFault},
		{
			name: "contended",
			fault: func(b *faultBackend) {
				b.casLosses.Store(int32(rotationMetaRetries))
				arm(&b.loseCAS, credentialPrefix+"k")
			},
			cause: ErrRotationMetaContended,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			svc, backend := newFaultBarrierService(t)
			require.NoError(t, svc.StoreCredential(ctx, "k", []byte("v1")))
			tc.fault(backend)

			err := svc.StoreCredential(ctx, "k", []byte("v2"))
			require.ErrorIs(t, err, tc.cause)
			disarmAll(backend)
			got, err := svc.store.Get(ctx, credentialPrefix+"k")
			require.NoError(t, err)
			assert.Equal(t, "v1", string(got))
		})
	}
}

// A staged value the metadata names is resumed only when it is for the
// next version and is still stored: one staged for another version, or one
// whose record is gone, is not resumed, and the due rotation asks the
// source for a value.
func TestRotateDue_UnusableStagedValueAsksTheSource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stage  func(next uint64) uint64
		record bool
	}{
		{name: "staged for another version", stage: func(next uint64) uint64 { return next + 5 }, record: true},
		{name: "staged record gone", stage: func(next uint64) uint64 { return next }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			svc, _ := newFaultRotationService(t)
			setFaultPolicy(t, svc, "k", 3)
			meta, err := svc.loadBarrierMeta(ctx, "k")
			require.NoError(t, err)
			_, next := rotationVersions(meta.CurrentVersion, true)
			staged := tc.stage(next)
			if tc.record {
				require.NoError(t, svc.store.Put(ctx, stagedKey("k", staged), []byte("stale")))
			}
			require.NoError(t, svc.updateBarrierMeta(ctx, "k", "stage", func(m *credentialMeta) bool {
				m.Staged = staged
				return true
			}))

			value, version, err := svc.stagedRotation(ctx, "k")
			require.NoError(t, err)
			assert.Nil(t, value)
			assert.Zero(t, version)

			var calls atomic.Int32
			countSource(svc, &calls)
			n, err := svc.RotateDue(ctx, rotationEpoch.Add(time.Hour))
			require.NoError(t, err)
			assert.Equal(t, 1, n)
			assert.Equal(t, int32(1), calls.Load(), "the source is asked for the value")
			current, _, err := svc.loadBarrierCurrent(ctx, "k")
			require.NoError(t, err)
			assert.NotEqual(t, "stale", string(current))
			assert.NotEqual(t, "k-initial", string(current))
		})
	}
}

// A cleanup clears the pending marker only when it still names that
// cleanup: one that cannot read the metadata fails and keeps the marker for
// the next check, and one whose marker was replaced by a later rotation's
// leaves that rotation's marker in place.
func TestCleanupRotation_MarkerKeptUnlessNamed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		marker string
		fault  func(*faultBackend)
		cause  error
	}{
		{name: "metadata unreadable", marker: "c1", fault: func(b *faultBackend) { arm(&b.failGet, metaKey("k")) }, cause: errInjectedFault},
		{name: "marker names a later cleanup", marker: "later"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			svc, backend := newFaultRotationService(t)
			setFaultPolicy(t, svc, "k", 3)
			require.NoError(t, svc.updateBarrierMeta(ctx, "k", "mark", func(m *credentialMeta) bool {
				m.PendingCleanup = tc.marker
				return true
			}))
			if tc.fault != nil {
				tc.fault(backend)
			}

			err := svc.cleanupRotation(ctx, "k", "c1")
			if tc.cause != nil {
				require.ErrorIs(t, err, tc.cause)
			} else {
				require.NoError(t, err)
			}
			disarmAll(backend)
			meta, err := svc.loadBarrierMeta(ctx, "k")
			require.NoError(t, err)
			assert.Equal(t, tc.marker, meta.PendingCleanup)
		})
	}
}

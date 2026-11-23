package credentials

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
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
				meta, err := svc.loadBarrierMeta(ctx, "k")
				require.NoError(t, err)
				require.NoError(t, svc.storeBarrier(ctx, "k", []byte("manual")))
				// The rotation's own delete of the staged value, after
				// the store's batch deleted it, fails.
				if failDelete {
					arm(&backend.failDelete, stagedKey("k", meta.Staged))
				}
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

// rotationState is what a scheduled pass must leave alone on a credential
// it could not rotate: the current value, the version list and the next
// due time.
type rotationState struct {
	value   string
	version uint64
	history int
	next    time.Time
}

func readRotationState(t *testing.T, svc *CredentialService, name string) rotationState {
	t.Helper()
	ctx := context.Background()
	current, _, err := svc.loadBarrierCurrent(ctx, name)
	require.NoError(t, err)
	meta, err := svc.loadBarrierMeta(ctx, name)
	require.NoError(t, err)
	_, next, err := svc.RotationStatus(ctx, name)
	require.NoError(t, err)
	return rotationState{value: string(current), version: meta.CurrentVersion, history: len(meta.History), next: next}
}

// TestRotateDue_NoValueSourceLeavesEachCredential pins the row item "a pass
// with no value source": every due credential is reported by name wrapping
// ErrNoRotationValueSource, and its value, versions and next due time are
// untouched.
func TestRotateDue_NoValueSourceLeavesEachCredential(t *testing.T) {
	svc, _ := newFaultRotationService(t)
	svc.rotationSource = nil
	setFaultPolicy(t, svc, "a", 3)
	setFaultPolicy(t, svc, "b", 3)
	before := map[string]rotationState{"a": readRotationState(t, svc, "a"), "b": readRotationState(t, svc, "b")}

	n, err := svc.RotateDue(t.Context(), rotationEpoch.Add(2*time.Hour))
	require.Zero(t, n)
	require.ErrorIs(t, err, ErrNoRotationValueSource)
	for _, name := range []string{"a", "b"} {
		requireRotationErrorFor(t, err, name)
		assert.Equal(t, before[name], readRotationState(t, svc, name), "%s changed by a pass with no value source", name)
	}
}

// TestRotateDue_SourceDeclinesOneNextStillRotates pins that a source with no
// value for one credential reports that credential alone: the next due
// credential in the same pass still rotates.
func TestRotateDue_SourceDeclinesOneNextStillRotates(t *testing.T) {
	svc, _ := newFaultRotationService(t)
	setFaultPolicy(t, svc, "a", 3)
	setFaultPolicy(t, svc, "b", 3)
	svc.rotationSource = func(_ context.Context, name, _ string) (*secure.Buffer, error) {
		if name == "a" {
			return nil, ErrNoRotationValueSource
		}
		return secure.NewBufferFromBytes([]byte("rotated-" + name))
	}
	beforeA := readRotationState(t, svc, "a")
	beforeB := readRotationState(t, svc, "b")

	n, err := svc.RotateDue(t.Context(), rotationEpoch.Add(2*time.Hour))
	require.Equal(t, 1, n)
	require.ErrorIs(t, err, ErrNoRotationValueSource)
	requireRotationErrorFor(t, err, "a")
	assert.Equal(t, beforeA, readRotationState(t, svc, "a"), "a changed although its source had no value")
	afterB := readRotationState(t, svc, "b")
	assert.Equal(t, "rotated-b", afterB.value)
	assert.Greater(t, afterB.version, beforeB.version, "b's version did not advance")
	assert.Equal(t, beforeB.history+1, afterB.history, "b's previous value was not kept")
	assert.True(t, afterB.next.After(beforeB.next), "b's next rotation did not advance")
}

// requireRotationErrorFor asserts err joins a *RotationError naming name.
func requireRotationErrorFor(t *testing.T, err error, name string) {
	t.Helper()
	joined, ok := err.(interface{ Unwrap() []error })
	require.True(t, ok, "error %v is not joined", err)
	for _, e := range joined.Unwrap() {
		var rotErr *RotationError
		if errors.As(e, &rotErr) && rotErr.Name == name {
			return
		}
	}
	t.Fatalf("error %v reports no *RotationError for %q", err, name)
}

// TestRotateDue_SourceErrorWithValueDestroysIt pins the cr1548a fix in
// rotateClaimed: a source that fails yet hands back a buffer still has that
// buffer destroyed, so no copy of a value it produced outlives the attempt.
func TestRotateDue_SourceErrorWithValueDestroysIt(t *testing.T) {
	svc, _ := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 3)
	before := readRotationState(t, svc, "k")
	var handed *secure.Buffer
	svc.rotationSource = func(context.Context, string, string) (*secure.Buffer, error) {
		buf, err := secure.NewBufferFromBytes([]byte("half-made"))
		require.NoError(t, err)
		handed = buf
		return buf, errInjectedFault
	}

	n, err := svc.RotateDue(t.Context(), rotationEpoch.Add(2*time.Hour))
	require.Zero(t, n)
	require.ErrorIs(t, err, errInjectedFault)
	require.NotNil(t, handed)
	assert.False(t, handed.IsAlive(), "the buffer a failing source returned was not destroyed")
	assert.Equal(t, before, readRotationState(t, svc, "k"))
}

// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// afTestClock is a settable clock safe for concurrent readers.
type afTestClock struct{ nanos atomic.Int64 }

func newAFTestClock(t time.Time) *afTestClock {
	c := &afTestClock{}
	c.Set(t)
	return c
}

func (c *afTestClock) Now() time.Time  { return time.Unix(0, c.nanos.Load()).UTC() }
func (c *afTestClock) Set(t time.Time) { c.nanos.Store(t.UnixNano()) }

// countingChallengeKV wraps a kvstore.AtomicKVStore and counts the read
// operations whose volume the sweep must keep independent of live
// challenges: Get calls, List calls, and the total number of keys every List
// returned. Embedding the AtomicKVStore interface forwards PutIfAbsent,
// CompareAndDelete and CompareAndSwap, so the wrapper keeps the full
// capability set the sweep asserts for.
//
// beforeCursorWrite, when set, runs once before the first cursor write
// (PutIfAbsent or CompareAndSwap on sysChallengeSweepCursorKey) reaches the
// store; the cursor-contention tests use it to interleave a second sweep
// between the first sweep's cursor read and its cursor write.
type countingChallengeKV struct {
	kvstore.AtomicKVStore
	gets       atomic.Int64
	lists      atomic.Int64
	listedKeys atomic.Int64

	hookFired         atomic.Bool
	beforeCursorWrite func()
}

var _ challengeSweepStore = (*countingChallengeKV)(nil)

func newCountingChallengeKV() *countingChallengeKV {
	return &countingChallengeKV{AtomicKVStore: kvstore.NewMemoryKVStore()}
}

func (k *countingChallengeKV) Get(ctx context.Context, key string) ([]byte, error) {
	k.gets.Add(1)
	return k.AtomicKVStore.Get(ctx, key)
}

func (k *countingChallengeKV) List(ctx context.Context, prefix string) ([]string, error) {
	k.lists.Add(1)
	keys, err := k.AtomicKVStore.List(ctx, prefix)
	k.listedKeys.Add(int64(len(keys)))
	return keys, err
}

func (k *countingChallengeKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	k.cursorHook(key)
	return k.AtomicKVStore.PutIfAbsent(ctx, key, value)
}

func (k *countingChallengeKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	k.cursorHook(key)
	return k.AtomicKVStore.CompareAndSwap(ctx, key, expected, next)
}

func (k *countingChallengeKV) cursorHook(key string) {
	if key != sysChallengeSweepCursorKey || k.beforeCursorWrite == nil {
		return
	}
	// Fire once, and not re-entrantly: the hook itself runs sweeps whose own
	// cursor writes pass through here.
	if k.hookFired.CompareAndSwap(false, true) {
		k.beforeCursorWrite()
	}
}

func (k *countingChallengeKV) resetCounts() {
	k.gets.Store(0)
	k.lists.Store(0)
	k.listedKeys.Store(0)
}

// newSweepTestService builds an authFactorService over kv with a settable
// clock and one signing mock factor.
func newSweepTestService(t *testing.T, kv AuthFactorKV, clock *afTestClock) *authFactorService {
	t.Helper()
	svc, err := NewAuthFactorService(AuthFactorServiceConfig{
		Factors: []authfactors.Factor{newSigningMockFactor(t, "piv")},
		KV:      kv,
	})
	if err != nil {
		t.Fatalf("NewAuthFactorService: %v", err)
	}
	impl := svc.(*authFactorService)
	impl.now = clock.Now
	return impl
}

// plantChallenge persists a challenge that expires at expiresAt, as though
// issued at issuedAt, and returns the identifier a caller would hold.
func plantChallenge(t *testing.T, svc *authFactorService, clock *afTestClock, id string, issuedAt, expiresAt time.Time) string {
	t.Helper()
	saved := clock.Now()
	clock.Set(issuedAt)
	defer clock.Set(saved)
	publicID, err := svc.persistChallenge(context.Background(), &authfactors.Challenge{
		ChallengeID:  id,
		EnrollmentID: "planted",
		Nonce:        []byte("nonce"),
		ExpiresAt:    expiresAt,
	})
	if err != nil {
		t.Fatalf("persistChallenge(%s): %v", id, err)
	}
	return publicID
}

// challengeRecordExists reports whether the record a challenge identifier
// names is still stored.
func challengeRecordExists(t *testing.T, kv AuthFactorKV, publicID string) bool {
	t.Helper()
	key, ok := parseChallengeID(publicID)
	if !ok {
		t.Fatalf("fixture: %q is not a well-formed challenge identifier", publicID)
	}
	data, err := kv.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get(%s): %v", key, err)
	}
	return len(data) > 0
}

// readSweepCursor returns the persisted cursor, or -1 when none exists.
func readSweepCursor(t *testing.T, kv AuthFactorKV) int64 {
	t.Helper()
	data, err := kv.Get(context.Background(), sysChallengeSweepCursorKey)
	if err != nil {
		t.Fatalf("Get(cursor): %v", err)
	}
	if len(data) == 0 {
		return -1
	}
	b, ok := parseChallengeBucket(string(data))
	if !ok {
		t.Fatalf("cursor %q is not a bucket number", data)
	}
	return b
}

// sweepHorizon is the last bucket a sweep at now may reclaim.
func sweepHorizon(now time.Time) int64 {
	return challengeBucket(now, now) - authFactorChallengeSweepGraceBuckets - 1
}

// TestAuthFactorService_ChallengeFactor_SweepCostIndependentOfLiveChallenges
// is the review finding's own measurement. The previous sweep bounded how
// many records it READ but began with an unbounded List of the whole
// challenge namespace, so every issuance scanned, materialized and returned
// every outstanding challenge. This plants 10,000 live challenges and
// requires ChallengeFactor's Get count, List count and number of keys listed
// to be IDENTICAL to the same call against a store holding none — and the
// listed-key count to be zero, since no live challenge may ever be listed.
func TestAuthFactorService_ChallengeFactor_SweepCostIndependentOfLiveChallenges(t *testing.T) {
	const liveChallenges = 10000
	ctx := context.Background()
	t0 := time.Now().UTC()

	type counts struct{ gets, lists, listedKeys int64 }
	measure := func(t *testing.T, planted int) []counts {
		clock := newAFTestClock(t0)
		kv := newCountingChallengeKV()
		svc := newSweepTestService(t, kv, clock)
		for i := range planted {
			plantChallenge(t, svc, clock, "live-"+strconv.Itoa(i), t0, t0.Add(time.Duration(10+i%60)*time.Minute))
		}
		enrollment, err := svc.EnrollFactor(ctx, "piv", "custodian_o1", authfactors.EnrollParams{})
		if err != nil {
			t.Fatalf("EnrollFactor: %v", err)
		}
		// Bring the cursor up to the present first. A cursor still walking
		// empty history would never come near a live bucket, and the
		// comparison below would pass without proving anything.
		for range 20 {
			svc.sweepExpiredChallenges(ctx)
		}
		if got, want := readSweepCursor(t, kv), sweepHorizon(t0); got != want {
			t.Fatalf("fixture: cursor = %d, want the horizon %d", got, want)
		}
		// Two minutes on, the horizon has moved and the sweep has buckets to
		// list, but every live challenge is still far ahead of it.
		clock.Set(t0.Add(2 * time.Minute))
		var out []counts
		for range 2 {
			kv.resetCounts()
			if _, err := svc.ChallengeFactor(ctx, enrollment.EnrollmentID); err != nil {
				t.Fatalf("ChallengeFactor: %v", err)
			}
			out = append(out, counts{kv.gets.Load(), kv.lists.Load(), kv.listedKeys.Load()})
		}
		return out
	}

	empty := measure(t, 0)
	loaded := measure(t, liveChallenges)
	for i := range empty {
		if loaded[i] != empty[i] {
			t.Errorf("call %d: store operations with %d live challenges = %+v, with none = %+v; cost must not depend on live challenges",
				i+1, liveChallenges, loaded[i], empty[i])
		}
		if loaded[i].listedKeys != 0 {
			t.Errorf("call %d: sweep listed %d keys; no live challenge may ever be listed", i+1, loaded[i].listedKeys)
		}
		if (i == 0 && loaded[i].lists == 0) || loaded[i].lists > authFactorChallengeSweepLists {
			t.Errorf("call %d: %d List calls, want at most %d (and at least 1 on the first call, whose horizon moved)", i+1, loaded[i].lists, authFactorChallengeSweepLists)
		}
		// loadEnrollment + cursor read + at most the record budget.
		if max := int64(2 + authFactorChallengeSweepRecords); loaded[i].gets > max {
			t.Errorf("call %d: %d Get calls, want at most %d", i+1, loaded[i].gets, max)
		}
	}
}

// TestAuthFactorService_ChallengeFactor_ReclaimsExpiredAcrossCalls plants
// unclaimed challenges across a month of history and asserts repeated
// issuance reclaims every one of them, never a live one, and never one
// within the grace margin behind the current bucket.
func TestAuthFactorService_ChallengeFactor_ReclaimsExpiredAcrossCalls(t *testing.T) {
	ctx := context.Background()
	t0 := time.Now().UTC()
	clock := newAFTestClock(t0)
	kv := newCountingChallengeKV()
	svc := newSweepTestService(t, kv, clock)

	var expired []string
	for i, age := range []time.Duration{30 * 24 * time.Hour, 26 * time.Hour, 2 * time.Hour, 10 * time.Minute} {
		for j := range 30 {
			issued := t0.Add(-age)
			id := "expired-" + strconv.Itoa(i) + "-" + strconv.Itoa(j)
			expired = append(expired, plantChallenge(t, svc, clock, id, issued, issued.Add(5*time.Minute)))
		}
	}
	// Expired 30 seconds ago: inside the grace margin, so not yet eligible.
	grace := plantChallenge(t, svc, clock, "grace", t0.Add(-30*time.Second), t0.Add(-30*time.Second))
	live := plantChallenge(t, svc, clock, "live", t0, t0.Add(5*time.Minute))

	enrollment, err := svc.EnrollFactor(ctx, "piv", "custodian_reclaim", authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}

	remaining := func() int {
		n := 0
		for _, id := range expired {
			if challengeRecordExists(t, kv, id) {
				n++
			}
		}
		return n
	}
	const maxCalls = 40
	calls := 0
	for ; calls < maxCalls && remaining() > 0; calls++ {
		kv.resetCounts()
		if _, err := svc.ChallengeFactor(ctx, enrollment.EnrollmentID); err != nil {
			t.Fatalf("ChallengeFactor: %v", err)
		}
		if got := kv.lists.Load(); got > authFactorChallengeSweepLists {
			t.Fatalf("call %d made %d List calls, want at most %d", calls+1, got, authFactorChallengeSweepLists)
		}
	}
	if n := remaining(); n != 0 {
		t.Fatalf("%d of %d expired challenges still stored after %d calls", n, len(expired), calls)
	}
	if !challengeRecordExists(t, kv, live) {
		t.Error("a live challenge was reclaimed")
	}
	if !challengeRecordExists(t, kv, grace) {
		t.Error("a challenge inside the grace margin was reclaimed")
	}
	// The walk over empty history after the last record may take a few
	// more calls; it must reach the horizon and stop there.
	for range 10 {
		if readSweepCursor(t, kv) == sweepHorizon(t0) {
			break
		}
		if _, err := svc.ChallengeFactor(ctx, enrollment.EnrollmentID); err != nil {
			t.Fatalf("ChallengeFactor: %v", err)
		}
	}
	if got, want := readSweepCursor(t, kv), sweepHorizon(t0); got != want {
		t.Errorf("cursor = %d after catching up, want the horizon %d", got, want)
	}

	// Once the grace margin has passed, the next issuance reclaims it too.
	clock.Set(t0.Add(3 * time.Minute))
	if _, err := svc.ChallengeFactor(ctx, enrollment.EnrollmentID); err != nil {
		t.Fatalf("ChallengeFactor: %v", err)
	}
	if challengeRecordExists(t, kv, grace) {
		t.Error("the grace-margin challenge was not reclaimed once its bucket aged past the margin")
	}
	if !challengeRecordExists(t, kv, live) {
		t.Error("a live challenge was reclaimed")
	}
}

// TestAuthFactorService_Sweep_RecordBudgetResumesMidBucket asserts that a
// bucket holding more expired records than one sweep's budget is drained
// across calls without the cursor passing it early.
func TestAuthFactorService_Sweep_RecordBudgetResumesMidBucket(t *testing.T) {
	ctx := context.Background()
	t0 := time.Now().UTC()
	clock := newAFTestClock(t0)
	kv := newCountingChallengeKV()
	svc := newSweepTestService(t, kv, clock)

	issued := t0.Add(-time.Hour)
	const planted = 3*authFactorChallengeSweepRecords + 5
	ids := make([]string, planted)
	for i := range ids {
		ids[i] = plantChallenge(t, svc, clock, "bulk-"+strconv.Itoa(i), issued, issued)
	}
	bucket := challengeBucket(issued, issued)

	for call := 1; ; call++ {
		kv.resetCounts()
		svc.sweepExpiredChallenges(ctx)
		if got := kv.gets.Load(); got > 1+authFactorChallengeSweepRecords {
			t.Fatalf("sweep %d made %d Get calls, want at most %d", call, got, 1+authFactorChallengeSweepRecords)
		}
		left := 0
		for _, id := range ids {
			if challengeRecordExists(t, kv, id) {
				left++
			}
		}
		if left == 0 {
			break
		}
		if cursor := readSweepCursor(t, kv); cursor >= bucket {
			t.Fatalf("cursor %d passed bucket %d while %d of its records remain", cursor, bucket, left)
		}
		if call > planted {
			t.Fatalf("records never drained: %d remain", left)
		}
	}
}

// TestAuthFactorService_Sweep_CursorCASContention interleaves two sweeps so
// that the one which read the cursor first writes it last. The second
// sweep's newer cursor must survive: a stale CompareAndSwap (or PutIfAbsent,
// when neither saw a cursor) is refused rather than moving the cursor back.
func TestAuthFactorService_Sweep_CursorCASContention(t *testing.T) {
	for _, tc := range []struct {
		name          string
		initialCursor bool
	}{
		{"absent cursor races on PutIfAbsent", false},
		{"present cursor races on CompareAndSwap", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			t0 := time.Now().UTC()
			kv := newCountingChallengeKV()

			earlyClock := newAFTestClock(t0)
			lateClock := newAFTestClock(t0.Add(time.Hour))
			early := newSweepTestService(t, kv, earlyClock)
			late := newSweepTestService(t, kv, lateClock)

			if tc.initialCursor {
				seed := []byte(formatChallengeBucket(sweepHorizon(t0.Add(-24 * time.Hour))))
				if err := kv.Put(ctx, sysChallengeSweepCursorKey, seed); err != nil {
					t.Fatalf("seed cursor: %v", err)
				}
			}
			old := plantChallenge(t, early, earlyClock, "old", t0.Add(-2*time.Hour), t0.Add(-2*time.Hour))
			mid := plantChallenge(t, early, earlyClock, "mid", t0.Add(30*time.Minute), t0.Add(30*time.Minute))

			// The early sweep reads the cursor, reclaims what is expired by
			// its clock, and — just before writing its cursor — the late
			// sweep runs to completion and writes a newer one.
			kv.beforeCursorWrite = func() {
				for range 20 {
					late.sweepExpiredChallenges(ctx)
				}
			}
			early.sweepExpiredChallenges(ctx)

			if got, want := readSweepCursor(t, kv), sweepHorizon(lateClock.Now()); got != want {
				t.Errorf("cursor = %d, want the later sweep's %d (the stale write must be refused)", got, want)
			}
			if challengeRecordExists(t, kv, old) || challengeRecordExists(t, kv, mid) {
				t.Error("an expired challenge survived both sweeps")
			}
		})
	}
}

// TestAuthFactorService_Sweep_ConcurrentSweepsAreSafe runs many sweeps from
// several services at once over one store under the race detector, then
// asserts every expired record is reclaimed, the live one is not, and the
// cursor settles on the horizon.
func TestAuthFactorService_Sweep_ConcurrentSweepsAreSafe(t *testing.T) {
	ctx := context.Background()
	t0 := time.Now().UTC()
	kv := newCountingChallengeKV()
	clock := newAFTestClock(t0)
	planter := newSweepTestService(t, kv, clock)

	var expired []string
	for i := range 200 {
		issued := t0.Add(-time.Duration(i+3) * time.Minute)
		expired = append(expired, plantChallenge(t, planter, clock, "c-"+strconv.Itoa(i), issued, issued))
	}
	live := plantChallenge(t, planter, clock, "live", t0, t0.Add(time.Hour))

	const workers, rounds = 8, 10
	var wg sync.WaitGroup
	for range workers {
		svc := newSweepTestService(t, kv, clock)
		wg.Go(func() {
			for range rounds {
				svc.sweepExpiredChallenges(ctx)
			}
		})
	}
	wg.Wait()
	for range 20 { // drain whatever lost cursor races left behind
		planter.sweepExpiredChallenges(ctx)
	}

	for _, id := range expired {
		if challengeRecordExists(t, kv, id) {
			t.Fatalf("expired challenge %s survived", id)
		}
	}
	if !challengeRecordExists(t, kv, live) {
		t.Error("a live challenge was reclaimed")
	}
	if got, want := readSweepCursor(t, kv), sweepHorizon(t0); got != want {
		t.Errorf("cursor = %d, want %d", got, want)
	}
}

// TestAuthFactorService_VerifyFactor_MalformedChallengeIDRefused asserts an
// identifier that cannot name a challenge record is refused as not-found
// without any store read.
func TestAuthFactorService_VerifyFactor_MalformedChallengeIDRefused(t *testing.T) {
	ctx := context.Background()
	kv := newCountingChallengeKV()
	svc := newSweepTestService(t, kv, newAFTestClock(time.Now()))

	for _, id := range []string{
		"",
		"plain-uuid-no-bucket",
		"123.short-bucket",
		"00000000000a.letter-in-bucket",
		"000000000001.",
		"000000000001/slash-not-dot",
		"-00000000001.negative",
	} {
		t.Run(id, func(t *testing.T) {
			kv.resetCounts()
			err := svc.VerifyFactor(ctx, "piv", id, &authfactors.Response{})
			var notFound *authfactors.EnrollmentNotFoundError
			if !errors.As(err, &notFound) {
				t.Fatalf("VerifyFactor(%q) = %v, want *authfactors.EnrollmentNotFoundError", id, err)
			}
			if got := kv.gets.Load(); got != 0 {
				t.Errorf("VerifyFactor(%q) made %d Get calls, want 0", id, got)
			}
		})
	}
}

// TestAuthFactorService_ChallengeRoundTripUsesBucketedID asserts the
// identifier ChallengeFactor returns names the record VerifyFactor consumes,
// and that the factor still receives the challenge under its own identifier.
func TestAuthFactorService_ChallengeRoundTripUsesBucketedID(t *testing.T) {
	ctx := context.Background()
	clock := newAFTestClock(time.Now())
	kv := newCountingChallengeKV()
	f := newSigningMockFactor(t, "piv")
	rec := &recordingFactor{signingMockFactor: f}
	svc, err := NewAuthFactorService(AuthFactorServiceConfig{Factors: []authfactors.Factor{rec}, KV: kv})
	if err != nil {
		t.Fatalf("NewAuthFactorService: %v", err)
	}
	svc.(*authFactorService).now = clock.Now

	enrollment, err := svc.EnrollFactor(ctx, "piv", "custodian_rt", authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}
	challenge, err := svc.ChallengeFactor(ctx, enrollment.EnrollmentID)
	if err != nil {
		t.Fatalf("ChallengeFactor: %v", err)
	}
	wantPrefix := formatChallengeBucket(challengeBucket(challenge.ExpiresAt, clock.Now())) + "."
	if len(challenge.ChallengeID) <= len(wantPrefix) || challenge.ChallengeID[:len(wantPrefix)] != wantPrefix {
		t.Fatalf("ChallengeID %q does not carry its expiry bucket %q", challenge.ChallengeID, wantPrefix)
	}
	if !challengeRecordExists(t, kv, challenge.ChallengeID) {
		t.Fatal("the identifier does not name the persisted record")
	}

	_ = svc.VerifyFactor(ctx, "piv", challenge.ChallengeID, &authfactors.Response{})
	if rec.verified == nil {
		t.Fatal("factor Verify was not reached")
	}
	if want := challenge.ChallengeID[len(wantPrefix):]; rec.verified.ChallengeID != want {
		t.Errorf("factor saw ChallengeID %q, want its own %q", rec.verified.ChallengeID, want)
	}
	if challengeRecordExists(t, kv, challenge.ChallengeID) {
		t.Error("the record survived VerifyFactor")
	}
}

// recordingFactor records the challenge Verify receives.
type recordingFactor struct {
	*signingMockFactor
	verified *authfactors.Challenge
}

func (f *recordingFactor) Verify(ctx context.Context, c *authfactors.Challenge, r *authfactors.Response) error {
	f.verified = c
	return f.signingMockFactor.Verify(ctx, c, r)
}

// emptyIDFactor issues a challenge with no identifier.
type emptyIDFactor struct{ *signingMockFactor }

func (f *emptyIDFactor) Challenge(ctx context.Context, e *authfactors.Enrollment) (*authfactors.Challenge, error) {
	c, err := f.signingMockFactor.Challenge(ctx, e)
	if err != nil {
		return nil, err
	}
	c.ChallengeID = ""
	return c, nil
}

// TestAuthFactorService_ChallengeFactor_EmptyFactorChallengeIDRefused
// asserts a factor that issues no identifier is refused before anything is
// stored, rather than filed under a key nothing could name.
func TestAuthFactorService_ChallengeFactor_EmptyFactorChallengeIDRefused(t *testing.T) {
	ctx := context.Background()
	kv := newCountingChallengeKV()
	svc, err := NewAuthFactorService(AuthFactorServiceConfig{
		Factors: []authfactors.Factor{&emptyIDFactor{newSigningMockFactor(t, "piv")}},
		KV:      kv,
	})
	if err != nil {
		t.Fatalf("NewAuthFactorService: %v", err)
	}
	enrollment, err := svc.EnrollFactor(ctx, "piv", "custodian_empty", authfactors.EnrollParams{})
	if err != nil {
		t.Fatalf("EnrollFactor: %v", err)
	}
	_, err = svc.ChallengeFactor(ctx, enrollment.EnrollmentID)
	var invalid *authfactors.EnrollmentInvalidError
	if !errors.As(err, &invalid) {
		t.Fatalf("ChallengeFactor = %v, want *authfactors.EnrollmentInvalidError", err)
	}
	if keys, _ := kv.AtomicKVStore.List(ctx, sysChallengePrefix); len(keys) != 0 {
		t.Errorf("records stored despite the refusal: %v", keys)
	}
}

// TestChallengeBucket_ClampsPastExpiryToNow asserts an already-expired
// challenge is filed in the current bucket, never a past one the sweep may
// already have passed.
func TestChallengeBucket_ClampsPastExpiryToNow(t *testing.T) {
	now := time.Unix(6000, 0)
	tests := []struct {
		name    string
		expires time.Time
		want    int64
	}{
		{"future expiry keeps its own bucket", now.Add(5 * time.Minute), 105},
		{"past expiry is filed now", now.Add(-time.Hour), 100},
		{"expiry equal to now", now, 100},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := challengeBucket(tc.expires, now); got != tc.want {
				t.Errorf("challengeBucket = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestParseChallengeID covers the identifier grammar in both directions.
func TestParseChallengeID(t *testing.T) {
	good := challengePublicID(29600000, "abc/def.ghi")
	key, ok := parseChallengeID(good)
	if !ok || key != sysChallengePrefix+"000029600000/abc/def.ghi" {
		t.Errorf("parseChallengeID(%q) = %q, %v", good, key, ok)
	}
	if b, ok := challengeKeyBucket(key); !ok || b != 29600000 {
		t.Errorf("challengeKeyBucket(%q) = %d, %v", key, b, ok)
	}
	for _, bad := range []string{"", "x", "000029600000", "000029600000.", "00002960000x.a", "000029600000-a"} {
		if _, ok := parseChallengeID(bad); ok {
			t.Errorf("parseChallengeID(%q) accepted a malformed identifier", bad)
		}
	}
	for _, bad := range []string{"other/000029600000/a", sysChallengePrefix + "0000296", sysChallengePrefix + "000029600000", sysChallengePrefix + "00002960000x/a"} {
		if _, ok := challengeKeyBucket(bad); ok {
			t.Errorf("challengeKeyBucket(%q) accepted a malformed key", bad)
		}
	}
}

// TestNextSweepBlock asserts each block is aligned, never crosses the
// horizon, and is the largest such block.
func TestNextSweepBlock(t *testing.T) {
	tests := []struct {
		next, horizon int64
		wantDigits    int
		wantLast      int64
	}{
		{0, 0, 12, 0},
		{0, 9, 11, 9},
		{0, 29_600_000, 5, 9_999_999},
		{20_000_000, 29_600_000, 6, 20_999_999},
		{29_600_000, 29_600_000, 12, 29_600_000},
		{29_599_990, 29_600_000, 11, 29_599_999},
		{29_599_995, 29_600_000, 12, 29_599_995},
		{100, 99_999, 10, 199},
	}
	for _, tc := range tests {
		prefix, last := nextSweepBlock(tc.next, tc.horizon)
		if want := sysChallengePrefix + formatChallengeBucket(tc.next)[:tc.wantDigits]; prefix != want || last != tc.wantLast {
			t.Errorf("nextSweepBlock(%d, %d) = %q, %d; want %q, %d", tc.next, tc.horizon, prefix, last, want, tc.wantLast)
		}
	}
}

// TestAuthFactorService_Sweep_StopsOnReadFailure asserts a record that
// cannot be read holds the cursor before its bucket, so it is retried rather
// than skipped.
func TestAuthFactorService_Sweep_StopsOnReadFailure(t *testing.T) {
	ctx := context.Background()
	t0 := time.Now().UTC()
	clock := newAFTestClock(t0)
	inner := newCountingChallengeKV()
	failing := &failingGetChallengeKV{countingChallengeKV: inner}
	svc := newSweepTestService(t, failing, clock)

	issued := t0.Add(-time.Hour)
	id := plantChallenge(t, svc, clock, "unreadable", issued, issued)
	failing.failKey, _ = parseChallengeID(id)

	for range 20 {
		svc.sweepExpiredChallenges(ctx)
	}
	if cursor := readSweepCursor(t, inner); cursor >= challengeBucket(issued, issued) {
		t.Errorf("cursor %d passed the bucket of a record that could not be read", cursor)
	}
	if !challengeRecordExists(t, inner, id) {
		t.Fatal("fixture: the unreadable record was deleted")
	}

	failing.failKey = ""
	for range 20 {
		svc.sweepExpiredChallenges(ctx)
	}
	if challengeRecordExists(t, inner, id) {
		t.Error("the record was not reclaimed once it became readable")
	}
}

var errSweepTestRead = errors.New("sweep test: read failed")

// failingGetChallengeKV fails Get for one key.
type failingGetChallengeKV struct {
	*countingChallengeKV
	failKey string
}

func (k *failingGetChallengeKV) Get(ctx context.Context, key string) ([]byte, error) {
	if k.failKey != "" && key == k.failKey {
		return nil, errSweepTestRead
	}
	return k.countingChallengeKV.Get(ctx, key)
}

// TestAuthFactorService_Sweep_ReclaimsUndecodableRecord asserts a record in
// an expired bucket that is not a challenge at all is reclaimed rather than
// holding the cursor forever.
func TestAuthFactorService_Sweep_ReclaimsUndecodableRecord(t *testing.T) {
	ctx := context.Background()
	t0 := time.Now().UTC()
	kv := newCountingChallengeKV()
	svc := newSweepTestService(t, kv, newAFTestClock(t0))

	key := challengeKey(challengeBucket(t0.Add(-time.Hour), t0.Add(-time.Hour)), "garbage")
	if err := kv.Put(ctx, key, []byte("not json")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	for range 20 {
		svc.sweepExpiredChallenges(ctx)
	}
	if data, _ := kv.Get(ctx, key); len(data) != 0 {
		t.Error("an undecodable record in an expired bucket was not reclaimed")
	}
}

// TestAuthFactorService_Sweep_UnparsableCursorRestarts asserts a corrupt
// cursor is replaced rather than wedging the sweep.
func TestAuthFactorService_Sweep_UnparsableCursorRestarts(t *testing.T) {
	ctx := context.Background()
	t0 := time.Now().UTC()
	clock := newAFTestClock(t0)
	kv := newCountingChallengeKV()
	svc := newSweepTestService(t, kv, clock)
	if err := kv.Put(ctx, sysChallengeSweepCursorKey, []byte("corrupt")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	id := plantChallenge(t, svc, clock, "old", t0.Add(-time.Hour), t0.Add(-time.Hour))
	for range 20 {
		svc.sweepExpiredChallenges(ctx)
	}
	if challengeRecordExists(t, kv, id) {
		t.Error("an expired challenge survived after a corrupt cursor")
	}
	if got, want := readSweepCursor(t, kv), sweepHorizon(t0); got != want {
		t.Errorf("cursor = %d, want %d", got, want)
	}
}

// TestAuthFactorService_Sweep_NonAtomicStoreIsANoOp asserts the sweep does
// nothing over a store that cannot perform the guarded writes it needs,
// rather than degrading to unguarded deletes that could race a concurrent
// VerifyFactor.
func TestAuthFactorService_Sweep_NonAtomicStoreIsANoOp(t *testing.T) {
	ctx := context.Background()
	t0 := time.Now().UTC()
	clock := newAFTestClock(t0)
	kv := newPutGetListOnlyKV()
	svc := newSweepTestService(t, kv, clock)

	id := plantChallenge(t, svc, clock, "old", t0.Add(-time.Hour), t0.Add(-time.Hour))
	for range 20 {
		svc.sweepExpiredChallenges(ctx)
	}
	if !challengeRecordExists(t, kv, id) {
		t.Error("the sweep deleted a record over a store with no atomic capability")
	}
}

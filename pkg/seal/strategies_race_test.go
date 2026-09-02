// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"context"
	"fmt"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"testing"

	quicraftseal "github.com/jeremyhahn/go-quicraft/pkg/seal"
	"github.com/stretchr/testify/require"
)

// Deliberately weak KDF work factors, for tests whose subject is NOT the KDF.
//
// The tests in this file exist to prove the b.strategies map is read and
// written under a lock. The passphrase derivation is only the writer's
// payload, and it dominates: at production settings Argon2id costs 128 MiB and
// four passes per derivation, and these tests run 60 of them while readers
// spin -- under -race, that OOM-killed pkg/seal on CI (go-qrdb#151, run 6900).
// The map race is identical at any work factor.
//
// The production numbers are go-quicraft's DefaultKDFParams() (Time 4,
// Memory 128 MiB, Threads 4) -- the values a SoftwareStrategy actually derives
// with. An earlier revision of this comment cited go-qrdb's own
// pkg/seal/kdf.defaultArgon2idMemory (64 MiB, Time 3), which is the wrong
// package and half the memory: pkg/seal/kdf is the versioned PASSWORD hasher
// used for PINs and has no non-test consumer of these constants. Re-measured
// 2026-09-02 (go-qrdb#151).
//
// These are TEST-ONLY values. Nothing in production reads them, and no
// production default is changed by this file.
const (
	cheapArgon2Time      uint32 = 1
	cheapArgon2MemoryKiB uint32 = 1024 // 1 MiB, against a 64 MiB default
	cheapArgon2Threads   uint8  = 1
	cheapPBKDF2Iters            = 1000 // against a 600,000 default
)

// cheapSoftwareStrategy builds a passphrase strategy with a deliberately low
// KDF work factor, selecting the constructor by FIPS mode.
//
// The mode check is load-bearing, not defensive. NewSoftwareStrategyWithParams
// selects Argon2id EXPLICITLY and fails with ErrKDFNotFIPSApproved under FIPS
// by design -- go-quicraft keeps the symbol in every build so consumers still
// compile, and refuses the call at runtime. Calling it unconditionally would
// therefore pass locally and fail only on the nightly FIPS leg, which is the
// slowest possible way to find out.
func cheapSoftwareStrategy(t *testing.T, passphrase string) *SoftwareStrategy {
	t.Helper()

	// go-quicraft enforces a production work-factor FLOOR on every derivation
	// (ErrKDFWeakParams), so cheap parameters are refused at seal time unless
	// the sanctioned test-only bypass is active. AllowWeakKDFParams exists for
	// exactly this case -- its own doc says the floors "must also be
	// bypassable from the test binaries of other packages that drive a real
	// barrier" -- and it panics outside a test binary, so it cannot weaken a
	// deployment. It returns a restore function; t.Cleanup keeps the bypass in
	// force for the whole test (Unseal derives too, not just Seal) and puts it
	// back afterwards.
	//
	// This is the reason the cheap parameters below cannot be used on their
	// own: without the bypass the seal fails outright rather than running
	// cheaply.
	t.Cleanup(quicraftseal.AllowWeakKDFParams())

	if FIPSEnabled() {
		s, err := quicraftseal.NewSoftwareStrategyFIPS([]byte(passphrase), cheapPBKDF2Iters)
		require.NoError(t, err, "cheap PBKDF2 strategy")
		return s
	}

	s, err := quicraftseal.NewSoftwareStrategyWithParams(
		[]byte(passphrase), cheapArgon2Time, cheapArgon2MemoryKiB, cheapArgon2Threads)
	require.NoError(t, err, "cheap Argon2id strategy")
	return s
}

// raceParticipants is how many goroutines the tests in this file spin opposite
// the loop under test -- readers in two of them, concurrent writers in the
// third.
//
// It is proportional to GOMAXPROCS because the previous fixed counts (8 and 4)
// are the mechanism behind this file's superlinear degradation (go-qrdb#151).
// Eight unbounded readers on a 24-core box have cores to spare; on a 2-core CI
// runner packing seven jobs they are eight runnable goroutines competing with
// the one writer they exist to race against, so that writer gets a shrinking
// slice, takes longer, and the readers spin for longer still.
//
// Measured on this box, ConcurrentStatusAndChangePassword under -race with the
// old fixed 8:
//
//	GOMAXPROCS=2  284.3s     GOMAXPROCS=4  152.9s     unconstrained  93.0s
//
// 3.1x from unconstrained to two cores for identical work. That ratio is the
// defect. The absolute numbers are dominated by the KDF and are NOT -- see the
// note on TestStrategiesMap_ConcurrentStatusAndChangePassword_NoRace.
//
// A race needs one goroutine on the other side, not eight. The floor of 2
// keeps a genuine multi-goroutine interleaving on a single-core runner; the
// ceiling of 4 stops a large host paying for participants that add no
// detection. Detection is re-verified by mutation at every count -- see the
// catch rates recorded on go-qrdb#151.
func raceParticipants() int {
	n := runtime.GOMAXPROCS(0) / 2
	if n < 2 {
		return 2
	}
	if n > 4 {
		return 4
	}
	return n
}

// spinOpposite starts raceParticipants() goroutines running body until stop is
// closed, and returns a WaitGroup that reports when they have all exited.
//
// Each goroutine yields with runtime.Gosched() after every call. That is the
// difference between contending with the loop under test and starving it:
// without the yield a spinner holds its P until preemption, and on a runner
// with fewer cores than goroutines the thing being raced against is what
// waits.
//
// There is no sleep here and there must not be one. These goroutines have to
// be genuinely concurrent with the operation under test for the detector to
// observe anything, which is precisely what a sleep would destroy.
func spinOpposite(stop <-chan struct{}, body func()) *sync.WaitGroup {
	var wg sync.WaitGroup
	for i := 0; i < raceParticipants(); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				body()
				runtime.Gosched()
			}
		}()
	}
	return &wg
}

// TestStrategiesMap_ConcurrentStatusAndChangePassword_NoRace proves the fix
// for the b.strategies concurrent read/write defect: Status() (and the other
// unlocked readers, Strategies() and ShamirStrategy()) used to read the
// strategies map with no lock while ChangePassword mutated it, which is a
// genuine "fatal error: concurrent map read and map write" (a process crash,
// not merely a benign data race) whenever a Status() poll landed concurrently
// with a password change. Run under `-race`: the detector must report
// nothing, and (independent of -race) the test itself must never hit Go's
// fatal concurrent-map-access crash.
//
// # Why this test still uses a production-cost fixture
//
// Its two siblings below build cheap strategies and cost ~6-8s. This one
// cannot, and the reason is not an oversight. ChangePassword constructs its
// REPLACEMENT strategy internally with the parameterless
// NewDeferredSoftwareStrategy (change_password.go), so it derives at
// go-quicraft's DefaultKDFParams() -- 128 MiB, four passes -- on every one of
// the 60 iterations, whatever the test injects. Measured 2026-09-02 under
// -race: ~1.29 GB peak RSS against ~570 MB for the cheap siblings, and that
// figure is flat across GOMAXPROCS 2/4/unconstrained, so it is the KDF and not
// the spinners.
//
// Making it cheap requires preserving a caller's KDF parameters across the
// re-seal, which is go-qrdb#156 -- itself blocked on go-quicraft#72, because
// upstream has no deferred constructor that accepts parameters and no accessor
// for reading them back. Do not "fix" this by cutting the 60 iterations: the
// iteration count is the race window, which is the coverage.
func TestStrategiesMap_ConcurrentStatusAndChangePassword_NoRace(t *testing.T) {
	// Collect eagerly for the duration of this test.
	//
	// The writer's payload is 60 Argon2id derivations at 128 MiB each. Those
	// buffers die immediately, but at the default GOGC=100 the heap roughly
	// doubles before the collector runs, so they pile up. Bounding the
	// participants (raceParticipants) stopped the writer being starved, which
	// means it now issues those derivations FASTER and the pile grows faster
	// still: measured at GOMAXPROCS=2, n=3 each, peak RSS went 1224 MB ->
	// 1492 MB while wall time went 284s -> 82s.
	//
	// Faster but hungrier is the wrong trade for this test specifically: it
	// was OOM-KILLED on CI, not merely slow (go-qrdb#151, run 6900), on a
	// runner packing seven jobs. Collecting more eagerly hands the memory back
	// and keeps most of the speedup -- same conditions, n=3:
	//
	//	baseline (8 unbounded)      284s   1224 MB
	//	bounded participants         82s   1492 MB
	//	bounded + this line         100s   1230 MB
	//
	// 2.8x faster than baseline with peak RSS back where it started.
	//
	// SetGCPercent returns the previous value, so this restores it on exit. It
	// is process-wide, which is safe here because this test does not call
	// t.Parallel() and therefore runs alone.
	defer debug.SetGCPercent(debug.SetGCPercent(20))

	ctx := context.Background()
	cfg := BarrierConfig{RootKeyPath: "test/root-key"}
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), cfg, testSoftwareStrategy(t))
	require.NoError(t, err)
	require.NoError(t, b.Initialize(ctx, Credentials{Secret: []byte("initial-password")}))

	stop := make(chan struct{})

	// Readers: hammer every code path that reads b.strategies —
	// Status/strategyOrPersisted, Strategies (full-map snapshot), and
	// ShamirStrategy (map lookup) — concurrently with the writer below.
	wg := spinOpposite(stop, func() {
		_ = b.Status()
		_ = b.Strategies()
		_ = b.ShamirStrategy()
	})

	// Writer: ChangePassword replaces the StrategyPassphrase entry in
	// b.strategies on every call (see change_password.go's setStrategy).
	for i := 0; i < 60; i++ {
		pw := fmt.Sprintf("password-%d", i)
		require.NoError(t, b.ChangePassword(ctx, Credentials{Secret: []byte(pw)}))
	}

	close(stop)
	wg.Wait()

	// The barrier must still be fully functional after the concurrent
	// hammering: readable, unseal-able with the final password.
	require.False(t, b.IsSealed())
	status := b.Status()
	require.Equal(t, StrategyPassphrase, status.Strategy)
}

// TestStrategiesMap_ConcurrentStatusAndUnseal_NoRace runs a seal/unseal cycle
// concurrently with Status() polling.
//
// WHAT IT DOES NOT DO, despite its name: it does not exercise the
// strategies-map race. Measured (go-qrdb#151) by deleting the RLock from
// strategyByID and re-running: this test passes 5/5 with the lock gone, while
// its ChangePassword sibling above catches the same mutant immediately with
// four DATA RACE reports.
//
// The reason is that nothing in a seal/unseal cycle WRITES b.strategies. The
// only setStrategy callers are registration (barrier.go:315), ChangePassword,
// Rekey, migrate and the Shamir install -- none of which run here. Eight
// readers spinning against a map nobody writes cannot report a race, so the
// original claim that this "proves the read path in Unseal is race-free
// against any concurrent strategies-map access" is not something this test can
// support.
//
// What it does still cover is the seal/unseal lifecycle under concurrent
// Status() polling -- the sealed-state flag and the barrier's own state --
// which is worth keeping. It is documented for what it is rather than renamed,
// because the name is referenced from go-qrdb#151 and the surrounding history;
// The map-race coverage this test's name promises now lives in
// TestStrategiesMap_ConcurrentUnsealAndInstall_NoRace below, which puts a real
// writer beside Unseal's lookup and is mutation-verified 10/10 (go-qrdb#158).
func TestStrategiesMap_ConcurrentStatusAndUnseal_NoRace(t *testing.T) {
	ctx := context.Background()
	base := NewMemoryBackend()
	cfg := BarrierConfig{RootKeyPath: "test/root-key"}
	// Cheap KDF: the subject is the strategies-map race, and Unseal resolves
	// the strategy from that map on every one of the 60 iterations below. The
	// derivation cost is incidental and, at production settings, fatal on a
	// loaded runner (go-qrdb#151).
	strategy := cheapSoftwareStrategy(t, "password")
	b, err := NewStorageBarrier(testLogger(), base, cfg, strategy)
	require.NoError(t, err)
	require.NoError(t, b.Initialize(ctx, Credentials{Secret: []byte("password")}))
	require.NoError(t, b.Seal())

	stop := make(chan struct{})
	wg := spinOpposite(stop, func() { _ = b.Status() })

	for i := 0; i < 60; i++ {
		require.NoError(t, b.Unseal(ctx, Credentials{Secret: []byte("password")}))
		require.NoError(t, b.Seal())
	}

	close(stop)
	wg.Wait()
}

// Unseal's strategies-map READ is safe against a concurrent install.
//
// This is the coverage go-qrdb#158 was filed for. Its neighbour above spins
// readers against a seal/unseal cycle that never writes b.strategies, so it
// cannot fail however the map is accessed. The missing ingredient was never a
// reader -- it was a concurrent WRITER, and this test supplies one.
//
// # The reader is Unseal itself
//
// Unseal resolves the sealing strategy through strategyByID (barrier.go), so
// each iteration of the seal/unseal cycle below performs the real lookup. It
// returns early only when the barrier is already unsealed, which is why the
// cycle re-seals every time rather than hammering Unseal from many goroutines:
// concurrent Unseals would mostly bounce off that early return without ever
// reaching the map.
//
// # Why the writer uses setStrategy directly
//
// Every real mutator -- ChangePassword, Rekey, the Shamir install, migrate --
// funnels through setStrategy, which barrier.go names as the mandatory write
// accessor alongside strategyByID. Driving the accessor covers all of them at
// once, where picking one caller would cover only that caller and drag its
// preconditions into a test about map access (ChangePassword, for instance,
// requires an UNSEALED barrier, which fights the cycle this reader needs).
//
// The install targets StrategyShamir, a different key from the one Unseal
// resolves, so the writer never disturbs the value the reader depends on. The
// race under test is on the map itself, not on any entry.
//
// Mutation-verified: deleting the RLock from strategyByID turns this red.
func TestStrategiesMap_ConcurrentUnsealAndInstall_NoRace(t *testing.T) {
	ctx := context.Background()
	cfg := BarrierConfig{RootKeyPath: "test/root-key"}
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), cfg, cheapSoftwareStrategy(t, "password"))
	require.NoError(t, err)
	require.NoError(t, b.Initialize(ctx, Credentials{Secret: []byte("password")}))
	require.NoError(t, b.Seal())

	shamir, err := NewShamirStrategy(2, 3)
	require.NoError(t, err)

	stop := make(chan struct{})
	var writes atomic.Int64

	// Writer: the mandatory write accessor, under a key the reader does not
	// resolve. This is what the neighbour test has never had.
	wg := spinOpposite(stop, func() {
		b.setStrategy(StrategyShamir, shamir)
		writes.Add(1)
	})

	// Reader: the real Unseal path, which looks the strategy up in the map on
	// every iteration.
	for i := 0; i < 60; i++ {
		require.NoError(t, b.Unseal(ctx, Credentials{Secret: []byte("password")}))
		require.NoError(t, b.Seal())
	}

	close(stop)
	wg.Wait()

	// Both sides must actually have run, or the test raced nothing -- which is
	// precisely how its neighbour shipped.
	require.Positive(t, writes.Load(), "the writer never ran")
	require.True(t, b.IsSealed(), "the barrier must end sealed")
}

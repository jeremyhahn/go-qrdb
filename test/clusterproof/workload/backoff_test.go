// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build clusterproof

package workload

import (
	"context"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
)

// pauses records every backoff pause instead of taking it, so no test here
// waits on a timer. When stopAt is positive, the stopAt-th pause cancels the
// run, which bounds a workload whose every attempt fails.
type pauses struct {
	mu     sync.Mutex
	got    []time.Duration
	stopAt int
	cancel context.CancelFunc
}

func (p *pauses) sleep(_ context.Context, d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.got = append(p.got, d)
	if p.stopAt > 0 && len(p.got) == p.stopAt {
		p.cancel()
	}
}

func (p *pauses) all() []time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]time.Duration(nil), p.got...)
}

// requireInBand fails unless d is in its step's upper half, [step/2, step].
func requireInBand(t *testing.T, i int, d, step time.Duration) {
	t.Helper()
	if d < step/2 || d > step {
		t.Fatalf("pause %d = %v, want within [%v, %v]", i, d, step/2, step)
	}
}

// runPaused runs one put-only client over cfg with the recording sleep.
func runPaused(t *testing.T, cfg Config, dial Dialer, stopAt int) (history.History, Stats, []time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &pauses{stopAt: stopAt, cancel: cancel}
	rec := history.NewRecorder(history.NewMonotonicClock())
	w, err := New(cfg, dial, rec)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w.sleep = p.sleep
	stats := w.Run(ctx)
	return rec.History(), stats, p.all()
}

func pausedConfig(endpoints ...string) Config {
	cfg := testConfig(endpoints...)
	cfg.Clients = 1
	cfg.OpsPerClient = 12
	cfg.Mix = Mix{Put: 1}
	cfg.RedialBackoff = Duration(time.Millisecond)
	cfg.MaxBackoff = Duration(8 * time.Millisecond)
	return cfg
}

func TestBackoffDoublesUpToLimit(t *testing.T) {
	b := newBackoff(100*time.Millisecond, 800*time.Millisecond, rand.New(rand.NewPCG(1, 2)), sleepContext)
	steps := []time.Duration{100, 200, 400, 800, 800, 800}
	for i, step := range steps {
		requireInBand(t, i, b.next(), step*time.Millisecond)
	}
}

func TestBackoffCapsAtLimitThatIsNotAPowerOfTwo(t *testing.T) {
	b := newBackoff(time.Second, 3*time.Second, rand.New(rand.NewPCG(3, 4)), sleepContext)
	steps := []time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 3 * time.Second}
	for i, step := range steps {
		requireInBand(t, i, b.next(), step)
	}
}

func TestBackoffJitterStaysInBounds(t *testing.T) {
	const step = 1000 * time.Nanosecond
	b := newBackoff(step, step, rand.New(rand.NewPCG(5, 6)), sleepContext)
	lo, hi := step, time.Duration(0)
	for i := range 4000 {
		d := b.next()
		requireInBand(t, i, d, step)
		lo, hi = min(lo, d), max(hi, d)
	}
	// Equal jitter spreads over the whole upper half: draws that clustered
	// would let clients that failed together retry together.
	if lo != step/2 || hi != step {
		t.Fatalf("draws spanned [%v, %v], want the whole band [%v, %v]", lo, hi, step/2, step)
	}
}

func TestBackoffJitterIsReproducibleBySeed(t *testing.T) {
	a := newBackoff(time.Millisecond, time.Second, rand.New(rand.NewPCG(7, 8)), sleepContext)
	b := newBackoff(time.Millisecond, time.Second, rand.New(rand.NewPCG(7, 8)), sleepContext)
	for i := range 20 {
		if x, y := a.next(), b.next(); x != y {
			t.Fatalf("pause %d: %v and %v from one seed", i, x, y)
		}
	}
}

func TestBackoffResetStartsOverFromBase(t *testing.T) {
	b := newBackoff(10*time.Millisecond, time.Second, rand.New(rand.NewPCG(9, 10)), sleepContext)
	for range 5 {
		b.next()
	}
	b.reset()
	requireInBand(t, 0, b.next(), 10*time.Millisecond)
	requireInBand(t, 1, b.next(), 20*time.Millisecond)
}

func TestBackoffResetBeforeAnyFailureIsHarmless(t *testing.T) {
	b := newBackoff(10*time.Millisecond, time.Second, rand.New(rand.NewPCG(11, 12)), sleepContext)
	b.reset()
	requireInBand(t, 0, b.next(), 10*time.Millisecond)
}

func TestBackoffWaitTakesTheDrawnPause(t *testing.T) {
	p := &pauses{}
	b := newBackoff(time.Hour, time.Hour, rand.New(rand.NewPCG(13, 14)), p.sleep)
	twin := newBackoff(time.Hour, time.Hour, rand.New(rand.NewPCG(13, 14)), sleepContext)
	b.wait(context.Background())
	b.wait(context.Background())
	got := p.all()
	if len(got) != 2 || got[0] != twin.next() || got[1] != twin.next() {
		t.Fatalf("wait slept %v, want the two pauses next draws", got)
	}
}

func TestSleepContextReturnsWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		sleepContext(ctx, time.Hour)
		close(done)
	}()
	<-done // a pause that ignored ctx would hang the test here
}

func TestSleepContextZeroPauseReturns(t *testing.T) {
	sleepContext(context.Background(), 0)
}

func TestRunBacksOffAfterEveryFailedOp(t *testing.T) {
	cfg := pausedConfig("n1")
	f := newFakeCluster(cfg.Endpoints...)
	f.setFault(func(context.Context, string, history.Kind) error {
		return status.Error(codes.ResourceExhausted, "rate limited")
	})
	h, stats, got := runPaused(t, cfg, f.dial, 0)
	if stats.Fail != uint64(cfg.OpsPerClient) {
		t.Fatalf("stats = %+v, want every put a definite failure", stats)
	}
	// The history is bounded by the pauses: one per failed op, never a
	// tight loop of retries, and the pauses grow to max_backoff and hold.
	if len(got) != len(h) {
		t.Fatalf("%d pauses for %d failed ops, want one each", len(got), len(h))
	}
	steps := []time.Duration{1, 2, 4, 8, 8, 8, 8, 8, 8, 8, 8, 8}
	for i, d := range got {
		requireInBand(t, i, d, steps[i]*time.Millisecond)
	}
}

func TestRunDoesNotPauseAfterSuccess(t *testing.T) {
	cfg := pausedConfig("n1")
	f := newFakeCluster(cfg.Endpoints...)
	_, stats, got := runPaused(t, cfg, f.dial, 0)
	if stats.OK != uint64(cfg.OpsPerClient) || len(got) != 0 {
		t.Fatalf("healthy run paused %d times (stats %+v), want never", len(got), stats)
	}
}

func TestRunResetsBackoffOnSuccess(t *testing.T) {
	cfg := pausedConfig("n1")
	cfg.OpsPerClient = 8
	f := newFakeCluster(cfg.Endpoints...)
	var n atomic.Int64
	f.setFault(func(context.Context, string, history.Kind) error {
		if n.Add(1) == 6 {
			return nil // ops 1-5 and 7-8 fail, op 6 succeeds
		}
		return status.Error(codes.PermissionDenied, "denied")
	})
	_, _, got := runPaused(t, cfg, f.dial, 0)
	steps := []time.Duration{1, 2, 4, 8, 8, 1, 2}
	if len(got) != len(steps) {
		t.Fatalf("paused %d times, want %d", len(got), len(steps))
	}
	for i, d := range got {
		requireInBand(t, i, d, steps[i]*time.Millisecond)
	}
}

func TestRunBacksOffBetweenFailedDials(t *testing.T) {
	cfg := pausedConfig("n1", "n2")
	f := newFakeCluster(cfg.Endpoints...)
	f.down["n1"].Store(true)
	f.down["n2"].Store(true)
	h, stats, got := runPaused(t, cfg, f.dial, 6)
	if len(h) != 0 || stats.DialFailures != 6 || len(got) != 6 {
		t.Fatalf("ops=%d dial failures=%d pauses=%d, want 0, 6, 6", len(h), stats.DialFailures, len(got))
	}
	steps := []time.Duration{1, 2, 4, 8, 8, 8}
	for i, d := range got {
		requireInBand(t, i, d, steps[i]*time.Millisecond)
	}
}

func TestRunDropsConnectionOnUnavailable(t *testing.T) {
	cfg := pausedConfig("n1")
	cfg.OpsPerClient = 6
	f := newFakeCluster(cfg.Endpoints...)
	var n atomic.Int64
	f.setFault(func(context.Context, string, history.Kind) error {
		if n.Add(1) <= 3 {
			return status.Error(codes.Unavailable, "connection reset")
		}
		return nil
	})
	h, stats, _ := runPaused(t, cfg, f.dial, 0)
	// Each Unavailable closes the connection, so the next op dials anew:
	// the first dial plus one per Unavailable.
	if dials := f.dials.Load(); dials != 4 {
		t.Fatalf("dialled %d times, want 4: an Unavailable did not drop the connection", dials)
	}
	// The write that met Unavailable stays indeterminate, and the redial
	// records nothing: the history holds exactly the operations sent.
	if stats.Indeterminate != 3 || stats.OK != 3 || len(h) != cfg.OpsPerClient {
		t.Fatalf("stats = %+v, ops = %d, want 3 indeterminate, 3 ok, %d ops", stats, len(h), cfg.OpsPerClient)
	}
}

func TestRunKeepsConnectionOnDefiniteRejection(t *testing.T) {
	cfg := pausedConfig("n1")
	cfg.OpsPerClient = 6
	f := newFakeCluster(cfg.Endpoints...)
	f.setFault(func(context.Context, string, history.Kind) error {
		return status.Error(codes.InvalidArgument, "bad key")
	})
	_, stats, _ := runPaused(t, cfg, f.dial, 0)
	if dials := f.dials.Load(); dials != 1 {
		t.Fatalf("dialled %d times, want 1: a definite rejection dropped a live connection", dials)
	}
	if stats.Fail != uint64(cfg.OpsPerClient) {
		t.Fatalf("stats = %+v, want every put failed", stats)
	}
}

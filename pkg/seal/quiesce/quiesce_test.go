// Copyright (c) 2026 Jeremy Hahn
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

package quiesce

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// drainBound is long enough that a drain the test expects to finish never
// runs out on a loaded runner; the tests never wait for it.
const drainBound = time.Minute

func TestDrainTimeoutError_ErrorNamesTheStores(t *testing.T) {
	err := &DrainTimeoutError{Stores: []string{"a", "b"}, Timeout: time.Second}
	msg := err.Error()
	for _, want := range []string{"a, b", "1s", ErrDrainTimeout.Error()} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q; want it to contain %q", msg, want)
		}
	}
}

func TestDrainTimeoutError_UnwrapsToErrDrainTimeout(t *testing.T) {
	var err error = &DrainTimeoutError{Stores: []string{"a"}}
	if !errors.Is(err, ErrDrainTimeout) {
		t.Fatalf("errors.Is(%v, ErrDrainTimeout) = false", err)
	}
	var typed *DrainTimeoutError
	if !errors.As(err, &typed) || typed.Stores[0] != "a" {
		t.Fatalf("errors.As = %v, %+v", err, typed)
	}
}

func TestGate_RegisterOnNilGateAdmitsAll(t *testing.T) {
	var g *Gate
	l := g.Register("db")
	if l != nil {
		t.Fatalf("Register on a nil gate = %v; want nil", l)
	}
	if err := l.Acquire(); err != nil {
		t.Fatalf("nil lease Acquire = %v; want nil", err)
	}
	l.Release()
	l.Unregister()
	if l.Name() != "" {
		t.Fatalf("nil lease Name = %q", l.Name())
	}
}

func TestGate_RegisterNamesTheLease(t *testing.T) {
	var g Gate
	a, b := g.Register("a"), g.Register("b")
	if a.Name() != "a" || b.Name() != "b" {
		t.Fatalf("names = %q, %q", a.Name(), b.Name())
	}
	if err := a.Acquire(); err != nil {
		t.Fatal(err)
	}
	if err := b.Acquire(); err != nil {
		t.Fatal(err)
	}
	if got := g.held(); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("held = %v; want [a b]", got)
	}
}

func TestGate_CloseRefusesNewWork(t *testing.T) {
	var g Gate
	l := g.Register("db")
	g.Close()
	if !g.Closed() {
		t.Fatal("Closed() = false after Close")
	}
	if err := l.Acquire(); !errors.Is(err, ErrSealing) {
		t.Fatalf("Acquire after Close = %v; want ErrSealing", err)
	}
	if got := g.held(); len(got) != 0 {
		t.Fatalf("a refused Acquire left a hold: %v", got)
	}
}

func TestGate_CloseIsIdempotent(t *testing.T) {
	var g Gate
	g.Close()
	g.Close()
	if !g.Closed() {
		t.Fatal("Closed() = false after two Closes")
	}
}

func TestGate_ReopenAdmitsWorkAgain(t *testing.T) {
	var g Gate
	l := g.Register("db")
	g.Close()
	g.Reopen()
	if g.Closed() {
		t.Fatal("Closed() = true after Reopen")
	}
	if err := l.Acquire(); err != nil {
		t.Fatalf("Acquire after Reopen = %v", err)
	}
	l.Release()
}

func TestGate_ReopenOnAnOpenGateIsANoOp(t *testing.T) {
	var g Gate
	g.Reopen()
	if g.Closed() {
		t.Fatal("Closed() = true on a never-closed gate")
	}
}

func TestGate_ClosedIsFalseOnTheZeroValue(t *testing.T) {
	var g Gate
	if g.Closed() {
		t.Fatal("the zero Gate is closed")
	}
}

func TestGate_DrainWithNothingHeldReturnsAtOnce(t *testing.T) {
	var g Gate
	g.Register("idle")
	g.Close()
	if err := g.Drain(drainBound); err != nil {
		t.Fatalf("Drain = %v", err)
	}
	if err := g.Drain(0); err != nil {
		t.Fatalf("Drain(0) with nothing held = %v", err)
	}
}

func TestGate_DrainWaitsForAHeldLease(t *testing.T) {
	var g Gate
	l := g.Register("db")
	if err := l.Acquire(); err != nil {
		t.Fatal(err)
	}
	g.Close()
	done := make(chan error, 1)
	go func() { done <- g.Drain(drainBound) }()
	select {
	case err := <-done:
		t.Fatalf("Drain returned %v with the lease held", err)
	default:
	}
	l.Release()
	if err := <-done; err != nil {
		t.Fatalf("Drain after Release = %v", err)
	}
}

func TestGate_DrainTimesOutNamingTheHeldStores(t *testing.T) {
	var g Gate
	held, idle := g.Register("held"), g.Register("idle")
	_ = idle
	if err := held.Acquire(); err != nil {
		t.Fatal(err)
	}
	g.Close()
	err := g.Drain(time.Millisecond)
	var typed *DrainTimeoutError
	if !errors.As(err, &typed) || !slices.Equal(typed.Stores, []string{"held"}) || typed.Timeout != time.Millisecond {
		t.Fatalf("Drain = %v; want a DrainTimeoutError naming [held]", err)
	}
	held.Release()
}

func TestGate_DrainWithNoBoundReportsAtOnce(t *testing.T) {
	var g Gate
	l := g.Register("db")
	if err := l.Acquire(); err != nil {
		t.Fatal(err)
	}
	g.Close()
	if err := g.Drain(-1); !errors.Is(err, ErrDrainTimeout) {
		t.Fatalf("Drain(-1) = %v; want ErrDrainTimeout", err)
	}
	l.Release()
}

func TestGate_DrainIgnoresAStaleSignal(t *testing.T) {
	var g Gate
	l := g.Register("db")
	g.Close()
	g.wake() // a stale signal from an earlier seal
	g.Reopen()
	if err := l.Acquire(); err != nil {
		t.Fatal(err)
	}
	g.Close()
	if err := g.Drain(time.Millisecond); !errors.Is(err, ErrDrainTimeout) {
		t.Fatalf("Drain = %v; want the stale signal not to end it", err)
	}
	l.Release()
}

func TestGate_DrainReturnsWhenTheLastHolderUnregisters(t *testing.T) {
	var g Gate
	l := g.Register("db")
	if err := l.Acquire(); err != nil {
		t.Fatal(err)
	}
	g.Close()
	done := make(chan error, 1)
	go func() { done <- g.Drain(drainBound) }()
	l.Release()
	l.Unregister()
	if err := <-done; err != nil {
		t.Fatalf("Drain = %v", err)
	}
}

func TestGate_HeldIsSorted(t *testing.T) {
	var g Gate
	for _, n := range []string{"c", "a", "b"} {
		if err := g.Register(n).Acquire(); err != nil {
			t.Fatal(err)
		}
	}
	if got := g.held(); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("held = %v; want sorted", got)
	}
}

func TestGate_HeldIsEmptyWithNoStores(t *testing.T) {
	var g Gate
	if got := g.held(); len(got) != 0 {
		t.Fatalf("held = %v", got)
	}
}

func TestGate_SignalIsMadeOnce(t *testing.T) {
	var g Gate
	if g.signal() != g.signal() {
		t.Fatal("signal() made two channels")
	}
	if cap(g.signal()) != 1 {
		t.Fatalf("signal() capacity = %d; want 1", cap(g.signal()))
	}
}

func TestGate_WakeNeverBlocks(t *testing.T) {
	var g Gate
	g.wake()
	g.wake() // the slot is full; this must not block
	select {
	case <-g.signal():
	default:
		t.Fatal("wake left no signal")
	}
}

func TestLease_AcquireAndReleaseBalance(t *testing.T) {
	var g Gate
	l := g.Register("db")
	for range 3 {
		if err := l.Acquire(); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		l.Release()
	}
	if n := l.n.Load(); n != 0 {
		t.Fatalf("count = %d; want 0", n)
	}
}

func TestLease_ReleaseWhileOpenSendsNoSignal(t *testing.T) {
	var g Gate
	l := g.Register("db")
	if err := l.Acquire(); err != nil {
		t.Fatal(err)
	}
	l.Release()
	select {
	case <-g.signal():
		t.Fatal("a release on an open gate signalled a drain")
	default:
	}
}

func TestLease_ReleaseToZeroWhileClosedSignals(t *testing.T) {
	var g Gate
	l := g.Register("db")
	if err := l.Acquire(); err != nil {
		t.Fatal(err)
	}
	g.Close()
	l.Release()
	select {
	case <-g.signal():
	default:
		t.Fatal("the last release while closed did not signal")
	}
}

func TestLease_UnregisterRemovesTheStore(t *testing.T) {
	var g Gate
	l := g.Register("db")
	l.Unregister()
	g.mu.Lock()
	n := len(g.leases)
	g.mu.Unlock()
	if n != 0 {
		t.Fatalf("leases = %d after Unregister", n)
	}
}

func TestLease_UnregisterTwiceIsSafe(t *testing.T) {
	var g Gate
	l := g.Register("db")
	g.Close()
	l.Unregister()
	l.Unregister()
	select {
	case <-g.signal():
	default:
		t.Fatal("Unregister on a closed gate did not wake the drain")
	}
}

// TestGate_ConcurrentAcquireAndSealNeverLosesAHold runs writers against a
// seal: every write admitted before the drain returns must have finished.
func TestGate_ConcurrentAcquireAndSealNeverLosesAHold(t *testing.T) {
	var g Gate
	l := g.Register("db")
	var writers sync.WaitGroup
	start := make(chan struct{})
	drained := make(chan struct{})
	for range 8 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			<-start
			for {
				if err := l.Acquire(); err != nil {
					return
				}
				select {
				case <-drained:
					t.Error("a write was admitted after the drain returned")
				default:
				}
				l.Release()
			}
		}()
	}
	close(start)
	g.Close()
	if err := g.Drain(drainBound); err != nil {
		t.Fatalf("Drain = %v", err)
	}
	close(drained)
	writers.Wait()
}

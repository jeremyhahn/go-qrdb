//go:build !integration

package raft

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
	"weak"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// A nil host has no gate: it admits every proposal and drains at once.
func TestProposalGate_NilHostHasNoGate(t *testing.T) {
	if g := proposalGateFor(nil); g != nil {
		t.Fatalf("proposalGateFor(nil) = %v, want nil", g)
	}
	ReopenProposals(nil)
}

// The first use makes the host's gate, and every later use reuses it.
func TestProposalGate_FirstUseCreatesLaterUseReuses(t *testing.T) {
	h := &quicraft.Host{}
	key := weak.Make(h)
	if _, ok := proposalGates.Load(key); ok {
		t.Fatal("a fresh host already has a gate")
	}
	first := proposalGateFor(h)
	if first == nil {
		t.Fatal("proposalGateFor made no gate")
	}
	if again := proposalGateFor(h); again != first {
		t.Fatal("a second use made a new gate instead of reusing the first")
	}
	runtime.KeepAlive(h)
}

// A first use that loses the store to a concurrent first use returns the
// winner's gate, so both share one drain.
func TestProposalGate_LoadOrStoreLosingTheRaceReturnsTheWinner(t *testing.T) {
	h := &quicraft.Host{}
	winner := &proposalGate{}
	winner.lease = winner.gate.Register("raft proposals")
	proposalGateStoreHook = func(key weak.Pointer[quicraft.Host]) {
		proposalGates.Store(key, winner)
	}
	t.Cleanup(func() { proposalGateStoreHook = nil })
	if got := proposalGateFor(h); got != winner {
		t.Fatal("the loser of the store kept its own gate, not the winner's")
	}
	proposalGateStoreHook = nil
	if got := proposalGateFor(h); got != winner {
		t.Fatal("a later use did not reuse the winner's gate")
	}
	runtime.KeepAlive(h)
}

// A value of the wrong type in proposalGates is a programming error.
func TestProposalGate_WrongStoredTypePanics(t *testing.T) {
	h := &quicraft.Host{}
	key := weak.Make(h)
	proposalGates.Store(key, "not a gate")
	t.Cleanup(func() { proposalGates.Delete(key) })
	defer func() {
		r := recover()
		msg, _ := r.(string)
		if !strings.Contains(msg, "want *proposalGate") {
			t.Fatalf("recovered %v, want the wrong-type panic", r)
		}
	}()
	proposalGateFor(h)
	t.Fatal("proposalGateFor did not panic on a wrong stored type")
}

// Once the seal closes the gate, the adapter refuses a proposal as sealing
// (shard paused) without submitting it; after the resume it submits again.
func TestProposalGate_AdapterRefusesWhileSealingAndAdmitsAfterResume(t *testing.T) {
	h := &quicraft.Host{}
	submitted := 0
	mock := &mockQuicraftHost{syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
		submitted++
		return sm.Result{Data: cmd}, nil
	}}
	a := NewWithInner(h, mock)
	if err := DrainProposals(h, time.Second); err != nil {
		t.Fatalf("DrainProposals: %v", err)
	}
	_, err := a.SyncPropose(context.Background(), 1, []byte("put"))
	if !errors.Is(err, ErrProposalsSealing) || !errors.Is(err, quicraft.ErrShardPaused) {
		t.Fatalf("SyncPropose while sealing = %v, want ErrProposalsSealing (shard paused)", err)
	}
	if submitted != 0 {
		t.Fatalf("a refused proposal reached the raft host %d times", submitted)
	}
	ReopenProposals(h)
	res, err := a.SyncPropose(context.Background(), 1, []byte("put"))
	if err != nil || string(res.Data) != "put" || submitted != 1 {
		t.Fatalf("SyncPropose after the resume = %v, %v (submitted %d), want it submitted once", res, err, submitted)
	}
	// The admitted proposal released the gate: a drain finds nothing held.
	if err := DrainProposals(h, 0); err != nil {
		t.Fatalf("DrainProposals after the proposal ended: %v", err)
	}
	ReopenProposals(h)
}

// The drain waits for a proposal the adapter admitted until its result.
func TestProposalGate_DrainWaitsForAnAdmittedAdapterProposal(t *testing.T) {
	h := &quicraft.Host{}
	entered, release := make(chan struct{}), make(chan struct{})
	mock := &mockQuicraftHost{syncProposeFn: func(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
		close(entered)
		<-release
		return sm.Result{Data: cmd}, nil
	}}
	a := NewWithInner(h, mock)
	done := make(chan error, 1)
	go func() {
		_, err := a.SyncPropose(context.Background(), 1, []byte("put"))
		done <- err
	}()
	<-entered
	drained := make(chan error, 1)
	go func() { drained <- DrainProposals(h, time.Minute) }()
	select {
	case err := <-drained:
		t.Fatalf("DrainProposals returned (%v) with the proposal still submitted", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("the admitted proposal: %v", err)
	}
	if err := <-drained; err != nil {
		t.Fatalf("DrainProposals after the result: %v", err)
	}
	ReopenProposals(h)
}

// A collected host's gate leaves proposalGates.
func TestProposalGate_CollectedHostIsForgotten(t *testing.T) {
	h := &quicraft.Host{}
	proposalGateFor(h)
	key := weak.Make(h)
	h = nil
	_ = h
	deadline := time.Now().Add(10 * time.Second)
	for {
		runtime.GC()
		if _, ok := proposalGates.Load(key); !ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a collected host's gate stayed in proposalGates")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

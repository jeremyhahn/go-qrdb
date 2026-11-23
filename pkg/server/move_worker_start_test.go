package server

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// blockingMoveSteps is a moveSteps whose first RunLocalSteps signals
// entered and then blocks until release closes, ignoring ctx the way a
// store close or copy that is already under way does.
type blockingMoveSteps struct {
	entered chan context.Context
	release chan struct{}
	once    sync.Once
}

func newBlockingMoveSteps() *blockingMoveSteps {
	return &blockingMoveSteps{entered: make(chan context.Context, 1), release: make(chan struct{})}
}

func (s *blockingMoveSteps) RunLocalSteps(ctx context.Context, _ string) error {
	first := false
	s.once.Do(func() { first = true })
	if first {
		s.entered <- ctx
		<-s.release
	}
	return nil
}

func (s *blockingMoveSteps) ReconcileOnce(context.Context) error { return nil }

func newStartTestWorker(steps moveSteps) *TenantMoveWorker {
	return &TenantMoveWorker{
		reconciler: steps,
		nodeID:     "n1",
		leads:      func() bool { return false },
		interval:   time.Millisecond,
		logger:     slog.Default(),
	}
}

// stopAsync runs stop in a goroutine and returns a channel closed when it
// returns.
func stopAsync(stop func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		stop()
		close(done)
	}()
	return done
}

func TestTenantMoveWorker_StopWaitsForInFlightTick(t *testing.T) {
	steps := newBlockingMoveSteps()
	stop := newStartTestWorker(steps).Start(context.Background())
	stepCtx := <-steps.entered
	stopped := stopAsync(stop)
	// stop has cancelled the step's context; the step still runs, so stop
	// must still be waiting for it.
	<-stepCtx.Done()
	select {
	case <-stopped:
		t.Fatal("stop returned while a move step was still running")
	default:
	}
	close(steps.release)
	<-stopped
}

func TestTenantMoveWorker_StopIdempotent(t *testing.T) {
	stop := newStartTestWorker(&countingMoveSteps{}).Start(context.Background())
	stop()
	stop()

	stop = newStartTestWorker(&countingMoveSteps{}).Start(context.Background())
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stop()
		}()
	}
	wg.Wait()
	stop()
}

func TestTenantMoveWorker_StopAfterParentCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stop := newStartTestWorker(&countingMoveSteps{}).Start(ctx)
	cancel()
	stop()
}

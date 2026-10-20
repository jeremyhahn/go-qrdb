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

package lifecycle

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// TestNodeWork_StopCancelsRunningWorkAndWaitsForIt proves stop returns only
// after the work it cancelled has returned.
func TestNodeWork_StopCancelsRunningWorkAndWaitsForIt(t *testing.T) {
	var w nodeWork
	entered := make(chan struct{})
	saw := make(chan error, 1)
	if !w.start(func(ctx context.Context) {
		close(entered)
		<-ctx.Done()
		saw <- ctx.Err()
	}) {
		t.Fatal("start on a running node refused the work")
	}
	<-entered

	w.stop()

	select {
	case err := <-saw:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("the work saw %v; want context.Canceled", err)
		}
	default:
		t.Fatal("stop returned before the work did")
	}
}

// TestNodeWork_StartAfterStopRunsNothing proves no work begins once the node
// is stopping, so none can outlive it.
func TestNodeWork_StartAfterStopRunsNothing(t *testing.T) {
	var w nodeWork
	w.stop()

	var ran atomic.Bool
	if w.start(func(context.Context) { ran.Store(true) }) {
		t.Fatal("start after stop accepted the work")
	}
	w.stop()
	if ran.Load() {
		t.Fatal("work started after stop ran")
	}
}

// TestNodeWork_ContextLivesUntilStop proves the zero value hands out a live
// context that stop cancels, and that stop may be called again.
func TestNodeWork_ContextLivesUntilStop(t *testing.T) {
	var w nodeWork
	ctx := w.context()
	if err := ctx.Err(); err != nil {
		t.Fatalf("context before stop: %v; want live", err)
	}

	w.stop()
	w.stop()

	if err := ctx.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("context after stop: %v; want context.Canceled", err)
	}
	if w.context() != ctx {
		t.Fatal("context after stop is a new context; want the cancelled one")
	}
}

// TestNode_StopCancelsTheNodesWorkBeforeAnyCloser proves the node's own work
// has ended before Stop closes anything it might use.
func TestNode_StopCancelsTheNodesWorkBeforeAnyCloser(t *testing.T) {
	n := &Node{}
	var workDone atomic.Bool
	n.work.start(func(ctx context.Context) {
		<-ctx.Done()
		workDone.Store(true)
	})
	var sawWorkDone bool
	n.addCloser(func() error {
		sawWorkDone = workDone.Load()
		return nil
	})

	if err := n.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !sawWorkDone {
		t.Fatal("a closer ran while the node's work was still running")
	}
}

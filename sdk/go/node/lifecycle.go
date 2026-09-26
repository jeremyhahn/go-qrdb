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

package node

import (
	"context"
	"sync"
	"sync/atomic"
)

// lifecycleGate serializes a Node's data-plane transitions — the boot Unseal
// completes, a system-scope encryption migration, and Close — and lets Close
// REFUSE the ones still waiting rather than queue behind them (go-qrdb#226).
//
// It is a one-slot semaphore plus a closing signal, not a mutex, because a
// mutex cannot be abandoned: Close holds the slot while it stops the servers,
// a graceful server stop waits for in-flight requests, and one of those
// requests may itself be a migration waiting for the slot. Close therefore
// signals closing BEFORE it waits for the slot, and a waiter selects on the
// slot, the signal and its own context, so it returns ErrNodeClosed instead of
// holding the stop open.
//
// The zero value is ready to use: a Node is sometimes constructed as a struct
// literal, and its gate must not depend on a constructor having run.
type lifecycleGate struct {
	initOnce  sync.Once
	slot      chan struct{}
	closing   chan struct{}
	closeOnce sync.Once

	// sharedMu orders enterShared's closing check and count against
	// beginClose's signal, so no shared holder is admitted after Close has
	// started waiting for them.
	sharedMu sync.Mutex
	shared   sync.WaitGroup

	// waiting counts acquire calls blocked on the slot. It is what lets a
	// caller — Close's tests among them — know a transition is queued rather
	// than merely requested.
	waiting atomic.Int32
}

func (g *lifecycleGate) init() {
	g.initOnce.Do(func() {
		g.slot = make(chan struct{}, 1)
		g.closing = make(chan struct{})
	})
}

// acquire takes the slot for a boot or migration. It returns ErrNodeClosed
// once Close has begun — whether that is already so, becomes so while this
// call waits, or races the slot becoming free — and ctx's error if ctx ends
// first. A nil return must be paired with release.
func (g *lifecycleGate) acquire(ctx context.Context) error {
	g.init()
	if g.isClosing() {
		return ErrNodeClosed
	}
	g.waiting.Add(1)
	defer g.waiting.Add(-1)
	select {
	case g.slot <- struct{}{}:
		// select picks at random among ready cases, so the slot can be won
		// in the same instant Close signalled. Close wins that race.
		if g.isClosing() {
			<-g.slot
			return ErrNodeClosed
		}
		return nil
	case <-g.closing:
		return ErrNodeClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// release returns the slot taken by acquire or beginClose.
func (g *lifecycleGate) release() {
	<-g.slot
}

// enterShared admits a transition that may run alongside others of its kind
// but not alongside Close — a tenant-scope migration, which has its own
// per-tenant guard. It returns ErrNodeClosed once Close has begun; a nil
// return must be paired with leaveShared.
func (g *lifecycleGate) enterShared() error {
	g.init()
	g.sharedMu.Lock()
	defer g.sharedMu.Unlock()
	if g.isClosing() {
		return ErrNodeClosed
	}
	g.shared.Add(1)
	return nil
}

// leaveShared ends a transition admitted by enterShared.
func (g *lifecycleGate) leaveShared() {
	g.shared.Done()
}

// beginClose signals closing, waits for every shared holder to leave, and then
// waits for the slot, so every in-flight boot or migration finishes before
// the caller tears down, and every waiter is refused rather than queued. It
// reports whether this was the first close. The caller must release the slot.
func (g *lifecycleGate) beginClose() bool {
	g.init()
	first := false
	g.sharedMu.Lock()
	g.closeOnce.Do(func() {
		close(g.closing)
		first = true
	})
	g.sharedMu.Unlock()
	g.shared.Wait()
	g.slot <- struct{}{}
	return first
}

// isClosing reports whether Close has begun.
func (g *lifecycleGate) isClosing() bool {
	g.init()
	select {
	case <-g.closing:
		return true
	default:
		return false
	}
}

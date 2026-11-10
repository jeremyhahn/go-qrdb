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

// Package quiesce drains the encrypted stores' in-flight work before an
// at-rest barrier seals.
//
// A Gate belongs to one barrier; each encrypted store over that barrier
// registers one Lease. Every flush, durable write and compaction the store
// admits holds its lease for its whole duration. Sealing closes the gate, so
// new work refuses at once, then drains: it waits, up to a bound, for the
// outstanding leases to be released before the key leaves memory.
//
// The write path is lock-free: Acquire and Release are one atomic add and one
// atomic load each. The gate's mutex is taken only to register or unregister
// a store and by the drain's scan.
package quiesce

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrSealing is the refusal of new work once the gate is closed.
var ErrSealing = errors.New("qrdb/seal/quiesce: the at-rest barrier is sealing")

// ErrDrainTimeout is wrapped by DrainTimeoutError.
var ErrDrainTimeout = errors.New("qrdb/seal/quiesce: in-flight work did not drain before the barrier sealed")

// DrainTimeoutError reports the stores still holding work when the drain's
// bound ran out.
type DrainTimeoutError struct {
	// Stores are the names of the leases still held, sorted.
	Stores []string
	// Timeout is the bound that ran out.
	Timeout time.Duration
}

func (e *DrainTimeoutError) Error() string {
	return fmt.Sprintf("%v: after %s, stores still in flight: %s",
		ErrDrainTimeout, e.Timeout, strings.Join(e.Stores, ", "))
}

func (e *DrainTimeoutError) Unwrap() error { return ErrDrainTimeout }

// Gate admits work while open and drains it once closed. The zero value is an
// open gate with no stores.
type Gate struct {
	closed atomic.Bool

	mu     sync.Mutex // guards leases
	leases map[*Lease]struct{}

	// drainMu serializes drains, so one drain consumes each signal.
	drainMu sync.Mutex
	once    sync.Once
	drained chan struct{} // one slot: a lease reached zero while closed
}

// Register adds a store's lease to the gate under name, which names the store
// in a DrainTimeoutError. A nil gate returns a nil lease, which admits all.
func (g *Gate) Register(name string) *Lease {
	if g == nil {
		return nil
	}
	l := &Lease{gate: g, name: name}
	g.mu.Lock()
	if g.leases == nil {
		g.leases = make(map[*Lease]struct{})
	}
	g.leases[l] = struct{}{}
	g.mu.Unlock()
	return l
}

// Close marks the gate sealing: every Acquire after it refuses with
// ErrSealing. It is idempotent.
func (g *Gate) Close() {
	g.closed.Store(true)
}

// Reopen admits work again after an unseal. It is idempotent.
func (g *Gate) Reopen() {
	g.closed.Store(false)
}

// Closed reports whether the gate refuses new work.
func (g *Gate) Closed() bool {
	return g.closed.Load()
}

// Drain waits until no lease is held, or until timeout runs out, when it
// returns a *DrainTimeoutError naming the stores still held. A timeout of
// zero or less does not wait. Drain does not close the gate: call Close
// first, or work admitted meanwhile keeps it waiting.
func (g *Gate) Drain(timeout time.Duration) error {
	g.drainMu.Lock()
	defer g.drainMu.Unlock()
	held := g.held()
	if len(held) == 0 {
		return nil
	}
	if timeout <= 0 {
		return &DrainTimeoutError{Stores: held, Timeout: timeout}
	}
	if drainWaitHook != nil {
		drainWaitHook(held)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	signal := g.signal()
	for {
		select {
		case <-signal:
		case <-timer.C:
			if held = g.held(); len(held) == 0 {
				return nil
			}
			return &DrainTimeoutError{Stores: held, Timeout: timeout}
		}
		if held = g.held(); len(held) == 0 {
			return nil
		}
	}
}

// held returns the sorted names of the leases with work in flight.
func (g *Gate) held() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var names []string
	for l := range g.leases {
		if l.n.Load() > 0 {
			names = append(names, l.name)
		}
	}
	slices.Sort(names)
	return names
}

// signal returns the drain-signal channel, made on first use.
func (g *Gate) signal() chan struct{} {
	g.once.Do(func() { g.drained = make(chan struct{}, 1) })
	return g.drained
}

// Lease is one store's hold on its gate. A nil Lease admits all work.
type Lease struct {
	gate *Gate
	name string
	n    atomic.Int64
	// gone is set by Unregister: the lease admits nothing more, and leaves
	// the gate once its last operation releases.
	gone atomic.Bool
}

// acquiredHook and drainWaitHook are test seams, nil in every build but the
// qrdb_sealtest one (sealtest_hold.go): acquiredHook runs inside each
// operation Acquire admits, and drainWaitHook runs when a drain with a
// timeout finds leases held, before it waits.
var (
	acquiredHook  func(*Lease)
	drainWaitHook func(held []string)
)

// Name is the store's name.
func (l *Lease) Name() string {
	if l == nil {
		return ""
	}
	return l.name
}

// Acquire holds the lease for one operation, or refuses with ErrSealing once
// the gate is closed or the lease unregistered. Every nil return must be
// paired with one Release.
func (l *Lease) Acquire() error {
	if l == nil {
		return nil
	}
	// Increment before the loads, and Close and Unregister store before
	// Drain and Unregister load the count: either this sees the gate closed
	// or the lease gone, or the drain sees the hold.
	l.n.Add(1)
	if l.gate.closed.Load() || l.gone.Load() {
		l.Release()
		return ErrSealing
	}
	if acquiredHook != nil {
		acquiredHook(l)
	}
	return nil
}

// Release ends one operation Acquire admitted. The last Release of an
// unregistered lease removes it from the gate.
func (l *Lease) Release() {
	if l == nil {
		return
	}
	if l.n.Add(-1) != 0 {
		return
	}
	g := l.gate
	if l.gone.Load() {
		g.mu.Lock()
		if l.n.Load() == 0 {
			delete(g.leases, l)
		}
		g.mu.Unlock()
	}
	if g.closed.Load() {
		g.wake()
	}
}

// Unregister ends the lease when its store closes: it admits nothing more,
// and an operation it already admitted keeps it in the gate's drain until
// that operation releases. A store's Close calls it before closing the
// store, so a seal never loses sight of work the store is still doing.
func (l *Lease) Unregister() {
	if l == nil {
		return
	}
	l.gone.Store(true)
	g := l.gate
	g.mu.Lock()
	if l.n.Load() == 0 {
		delete(g.leases, l)
	}
	g.mu.Unlock()
	if g.closed.Load() {
		g.wake()
	}
}

// wake signals a waiting drain to rescan, without blocking.
func (g *Gate) wake() {
	select {
	case g.signal() <- struct{}{}:
	default:
	}
}

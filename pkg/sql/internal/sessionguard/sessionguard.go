//go:build qrdb_sql

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

// Package sessionguard closes a SQL wire connection when a seal reaches it.
//
// A SQL wire session outlives the statement that opened it, and some of what
// it holds is served without asking the per-statement seal gate again: a
// MySQL server-side cursor is fetched inside the protocol library, never
// reaching the query handler, and a transaction holds its locks across
// statements. The per-statement gate refuses the NEXT statement; only closing
// the connection ends what is already open. So every SQL listener -- MySQL and
// PostgreSQL alike -- registers each connection here when its session is
// built, and the guard closes it the moment the node's seal probe closes or a
// cluster seal or cut-off ends its subscription.
package sessionguard

import (
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
)

// DefaultProbeInterval is how often a guard asks the seal probe. The probe is
// an atomic read; the interval bounds how long a connection serves after a
// node-local seal, which ends no stream subscription.
const DefaultProbeInterval = 250 * time.Millisecond

// Probe reports whether the node refuses every request: its barrier sealed,
// or a cluster seal holding it, cutting it off or not yet confirmed.
type Probe interface {
	IsSealed() bool
}

// SealedError is the refusal of a session opened while the node is sealed.
// Each listener answers it in its own wire form.
type SealedError struct{}

// Error implements error.
func (e *SealedError) Error() string { return "sessionguard: the node is sealed" }

// Guard tracks the open connections of one SQL listener and closes them when
// a seal reaches them. The zero Guard is not usable; build one with New.
type Guard struct {
	probe    Probe
	streams  *clusterseal.Streams
	interval time.Duration

	mu       sync.Mutex
	sessions map[*Session]struct{}

	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
}

// New returns a guard over probe and streams, either of which may be nil, that
// asks probe every interval (DefaultProbeInterval when interval is not
// positive). Start runs it.
func New(probe Probe, streams *clusterseal.Streams, interval time.Duration) *Guard {
	if interval <= 0 {
		interval = DefaultProbeInterval
	}
	return &Guard{
		probe:    probe,
		streams:  streams,
		interval: interval,
		sessions: make(map[*Session]struct{}),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Start runs the probe watch until Stop. A guard with no probe watches
// nothing, but Stop must still be called.
func (g *Guard) Start() {
	go g.watchProbe()
}

// Stop ends the probe watch and waits for it. It does not close sessions.
func (g *Guard) Stop() {
	g.stopOnce.Do(func() { close(g.stop) })
	<-g.done
}

// watchProbe closes every session while the probe reports sealed.
func (g *Guard) watchProbe() {
	defer close(g.done)
	if g.probe == nil {
		<-g.stop
		return
	}
	ticker := time.NewTicker(g.interval)
	defer ticker.Stop()
	for {
		select {
		case <-g.stop:
			return
		case <-ticker.C:
			if g.probe.IsSealed() {
				g.endAll()
			}
		}
	}
}

// refuses is a session open's refusal: the probe is closed.
func (g *Guard) refuses() error {
	if g.probe != nil && g.probe.IsSealed() {
		return &SealedError{}
	}
	return nil
}

// Open registers a connection of tenantID that closeConn closes, and returns
// its Session, which the listener releases when the connection closes. While
// the node is sealed it refuses with *SealedError.
//
// It subscribes before it asks the probe (clusterseal.Streams
// SubscribeTenantServing), so a seal landing as the session opens either
// refuses it or ends it: a seal closes its gate before it ends streams.
func (g *Guard) Open(tenantID string, closeConn func()) (*Session, error) {
	s := &Session{guard: g, closeConn: closeConn, released: make(chan struct{})}
	if g.streams != nil {
		sub, err := g.streams.SubscribeTenantServing(tenantID, g.refuses)
		if err != nil {
			return nil, err
		}
		s.sub = sub
	} else if err := g.refuses(); err != nil {
		return nil, err
	}
	g.mu.Lock()
	g.sessions[s] = struct{}{}
	g.mu.Unlock()
	if s.sub != nil {
		go s.endOnSeal()
	}
	return s, nil
}

// endAll closes every open session.
func (g *Guard) endAll() {
	g.mu.Lock()
	open := make([]*Session, 0, len(g.sessions))
	for s := range g.sessions {
		open = append(open, s)
	}
	g.mu.Unlock()
	for _, s := range open {
		s.end()
	}
}

// Len reports how many sessions are open.
func (g *Guard) Len() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.sessions)
}

// Session is one open connection's registration.
type Session struct {
	guard     *Guard
	closeConn func()
	sub       *clusterseal.Subscription

	endOnce     sync.Once
	releaseOnce sync.Once
	released    chan struct{}
}

// endOnSeal closes the connection when its subscription ends, until the
// session is released.
func (s *Session) endOnSeal() {
	select {
	case <-s.sub.Done():
		s.end()
	case <-s.released:
	}
}

// end closes the connection, once. The listener's own close handling then
// releases the session.
func (s *Session) end() {
	s.endOnce.Do(s.closeConn)
}

// Release forgets the session; the listener calls it when the connection
// closes, for any reason. It is idempotent.
func (s *Session) Release() {
	s.releaseOnce.Do(func() {
		close(s.released)
		s.guard.mu.Lock()
		delete(s.guard.sessions, s)
		s.guard.mu.Unlock()
		if s.sub != nil {
			s.sub.Release()
		}
	})
}

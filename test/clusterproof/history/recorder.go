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

package history

import (
	"sync"
	"sync/atomic"
	"time"
)

// Clock is the harness's one time source. Every call and return in a history
// is read from the same Clock, so the real-time order checkers rely on is the
// order one process observed, never a comparison of two hosts' wall clocks.
type Clock interface {
	// Now returns nanoseconds since an arbitrary fixed origin. It never
	// goes backwards.
	Now() int64
}

// monotonicClock reads Go's monotonic clock relative to its creation, so wall
// clock steps -- including ones a nemesis injects -- cannot reorder records.
type monotonicClock struct{ origin time.Time }

// NewMonotonicClock returns a Clock whose origin is the moment of the call.
func NewMonotonicClock() Clock { return monotonicClock{origin: time.Now()} }

func (c monotonicClock) Now() int64 { return int64(time.Since(c.origin)) }

// Recorder assembles a History from many concurrent clients. Each client
// records into its own Log, so the hot path takes no lock; only the shared ID
// sequence is touched, atomically.
type Recorder struct {
	clock  Clock
	nextID atomic.Uint64

	mu   sync.Mutex // guards logs; taken only to register a client
	logs []*Log
}

// NewRecorder returns a Recorder reading call and return times from clock.
func NewRecorder(clock Clock) *Recorder { return &Recorder{clock: clock} }

// Clock returns the recorder's clock.
func (r *Recorder) Clock() Clock { return r.clock }

// Log registers client and returns the log it records into. A Log belongs to
// one goroutine.
func (r *Recorder) Log(client int) *Log {
	l := &Log{rec: r, client: client}
	r.mu.Lock()
	r.logs = append(r.logs, l)
	r.mu.Unlock()
	return l
}

// History merges every client's log into one sorted History. Call it only
// once every client has stopped recording.
func (r *Recorder) History() History {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, l := range r.logs {
		n += len(l.ops)
	}
	h := make(History, 0, n)
	for _, l := range r.logs {
		h = append(h, l.ops...)
	}
	h.Sort()
	return h
}

// Log is one client's sequence of operations.
type Log struct {
	rec    *Recorder
	client int
	ops    []Op
}

// Pending is an operation that has been called and has not yet returned.
type Pending struct {
	op Op
}

// ID returns the pending operation's ID.
func (p Pending) ID() uint64 { return p.op.ID }

// Call stamps the start of an operation. It must be called immediately before
// the request is issued, so the call time is no later than the effect.
func (l *Log) Call(kind Kind, key string, in *Input) Pending {
	return Pending{op: Op{
		ID:     l.rec.nextID.Add(1),
		Client: l.client,
		Kind:   kind,
		Key:    key,
		Input:  in,
		CallNS: l.rec.clock.Now(),
	}}
}

// Return stamps the end of p and records it. It must be called immediately
// after the response (or the failure) arrives, so the return time is no
// earlier than the effect. out is kept only on an OutcomeOK record; err, when
// non-nil, is kept as text for the reader of the evidence.
func (l *Log) Return(p Pending, outcome Outcome, out *Output, err error) Op {
	op := p.op
	op.ReturnNS = l.rec.clock.Now()
	op.Outcome = outcome
	if outcome == OutcomeOK {
		op.Output = out
	}
	if err != nil {
		op.Error = err.Error()
	}
	l.ops = append(l.ops, op)
	return op
}

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
	"strconv"
	"sync"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
)

// keyspace schedules the run's keys the way Jepsen's independent checker
// does: Keys slots, each holding one live key, and a key retired for a fresh
// one before its history grows past what the linearizability search decides.
//
// A key is retired once it has taken maxOps operations, or when a write
// issued on it could take its indeterminate writes past maxIndeterminate.
// The second bound counts each write in flight as if it will end
// indeterminate, so concurrent clients cannot overshoot it: a write that
// later ends indeterminate on a retired key was already counted when the key
// was retired. A slot's first key is "<prefix>/k<slot>", so a run that
// never retires a key uses the names it always did; its successors are
// "<prefix>/k<slot>-<generation>".
type keyspace struct {
	prefix           string
	maxOps           int
	maxIndeterminate int

	mu    sync.Mutex
	slots []keySlot
	used  []string // every key handed out, in order
}

// keySlot is the live key of one slot.
type keySlot struct {
	gen int
	ops int
	// exposure is the slot's indeterminate writes plus its writes in
	// flight: the most indeterminate writes the key can end with.
	exposure int
}

// keyLease is one operation's hold on a key, returned by acquire.
type keyLease struct {
	slot  int
	gen   int
	write bool
}

func newKeyspace(prefix string, slots, maxOps, maxIndeterminate int) *keyspace {
	ks := &keyspace{
		prefix:           prefix,
		maxOps:           maxOps,
		maxIndeterminate: maxIndeterminate,
		slots:            make([]keySlot, slots),
	}
	for i := range ks.slots {
		ks.used = append(ks.used, ks.name(i, 0))
	}
	return ks
}

func (ks *keyspace) name(slot, gen int) string {
	n := ks.prefix + "/k" + strconv.Itoa(slot)
	if gen > 0 {
		n += "-" + strconv.Itoa(gen)
	}
	return n
}

// acquire returns the key an operation on slot uses, retiring the slot's
// live key first if the operation would take it past a bound. A write is any
// operation that may change the key.
func (ks *keyspace) acquire(slot int, write bool) (string, keyLease) {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	s := &ks.slots[slot]
	if s.ops >= ks.maxOps || (write && s.exposure >= ks.maxIndeterminate) {
		*s = keySlot{gen: s.gen + 1}
		ks.used = append(ks.used, ks.name(slot, s.gen))
	}
	s.ops++
	if write {
		s.exposure++
	}
	return ks.name(slot, s.gen), keyLease{slot: slot, gen: s.gen, write: write}
}

// release ends l's operation with outcome. A write that was answered no
// longer counts against its key; one that ended indeterminate does for good.
func (ks *keyspace) release(l keyLease, outcome history.Outcome) {
	if !l.write || outcome == history.OutcomeIndeterminate {
		return
	}
	ks.mu.Lock()
	defer ks.mu.Unlock()
	if s := &ks.slots[l.slot]; s.gen == l.gen {
		s.exposure--
	}
}

// all returns every key handed out so far.
func (ks *keyspace) all() []string {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	return append([]string(nil), ks.used...)
}

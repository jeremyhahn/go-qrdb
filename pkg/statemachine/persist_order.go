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

package statemachine

import "sync"

// persistOrder keeps a shard's asynchronous writes reaching storage in the
// order the log applied them.
//
// The two sharded state machines write to their cache under the shard lock and
// then persist on an independent goroutine. Independent is the problem: those
// goroutines race, so storage can apply them in any order at all. Apply
// Delete(k) and then PutIfAbsent(k, "new") and the conditional write returns
// ResultStored with "new" in the cache, while the delete's goroutine may finish
// AFTER the set's — leaving storage with no key. The accepted write is gone at
// the next restart, and nothing reported a fault. The same inversion loses an
// overwrite when an older Put finishes last (go-qrdb#86).
//
// It matters more for the guarded verb than for a plain Put, which is how it
// surfaced: PutIfAbsent both REPORTS acceptance and, on a cache miss, reads
// storage back to evaluate its guard. Stale storage therefore does not just
// lose a value, it decides future guards wrongly.
//
// A ticket is drawn while the shard's data lock is held — the point at which
// apply order is already established — and each writer waits for its turn
// before touching storage. Writes stay asynchronous, so apply never blocks on
// the disk; they simply land in the order they were applied. Ordering is
// per-shard, and a key belongs to exactly one shard, so it is per-key ordering
// where it counts.
type persistOrder struct {
	mu     sync.Mutex
	cond   *sync.Cond
	issued uint64
	served uint64
}

// init prepares the condition variable. Called once per shard at construction.
func (p *persistOrder) init() {
	p.cond = sync.NewCond(&p.mu)
}

// ticket reserves the next place in this shard's persistence order.
//
// It must be drawn while the caller holds the shard's data lock, so that the
// order of tickets is the order of applies. Drawing it afterwards would race
// exactly as the unordered goroutines did.
func (p *persistOrder) ticket() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	t := p.issued
	p.issued++
	return t
}

// run waits until ticket is next, performs fn, and releases the writer behind
// it. fn is the storage operation.
//
// The shard's data lock is NOT held here — it is released before the writer
// goroutine starts — so a slow disk delays only later writes to the same
// shard, never an apply.
func (p *persistOrder) run(ticket uint64, fn func()) {
	p.mu.Lock()
	for p.served != ticket {
		p.cond.Wait()
	}
	p.mu.Unlock()

	// fn runs OUTSIDE the ordering lock. Holding it across the storage call
	// would be equivalent, but a panic in fn would then leave every later
	// writer for this shard blocked forever with the lock held; this way the
	// deferred advance still runs.
	defer func() {
		p.mu.Lock()
		p.served++
		p.cond.Broadcast()
		p.mu.Unlock()
	}()
	fn()
}

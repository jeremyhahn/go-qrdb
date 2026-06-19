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

package keydelivery

import (
	"encoding/hex"
	"sync"
	"time"
)

// ReplayGuard tracks recently-seen delivery nonces so a captured envelope
// cannot be re-applied within its freshness window. Entries self-expire after
// the retention period (kept ≥ the envelope TTL so a guard restart cannot widen
// the replay window beyond what the TTL already permits). It is safe for
// concurrent use; key delivery is rare, so a mutex-guarded map is clearest.
type ReplayGuard struct {
	mu        sync.Mutex
	seen      map[string]time.Time
	retention time.Duration
	now       func() time.Time
}

// NewReplayGuard returns a guard that remembers nonces for the given retention.
// A non-positive retention falls back to DefaultTTL.
func NewReplayGuard(retention time.Duration) *ReplayGuard {
	if retention <= 0 {
		retention = DefaultTTL
	}
	return &ReplayGuard{
		seen:      make(map[string]time.Time),
		retention: retention,
		now:       time.Now,
	}
}

// observe records nonce as seen now and reports whether it had already been
// seen within the retention window (true == replay). Expired entries are
// evicted opportunistically on each call.
func (g *ReplayGuard) observe(nonce []byte) bool {
	key := hex.EncodeToString(nonce)
	now := g.now()
	cutoff := now.Add(-g.retention)

	g.mu.Lock()
	defer g.mu.Unlock()

	for k, t := range g.seen {
		if t.Before(cutoff) {
			delete(g.seen, k)
		}
	}
	if t, ok := g.seen[key]; ok && !t.Before(cutoff) {
		return true
	}
	g.seen[key] = now
	return false
}

// Len reports the number of live tracked nonces (after lazy eviction). Intended
// for observability and tests.
func (g *ReplayGuard) Len() int {
	now := g.now()
	cutoff := now.Add(-g.retention)
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, t := range g.seen {
		if !t.Before(cutoff) {
			n++
		}
	}
	return n
}

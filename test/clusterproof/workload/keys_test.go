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
	"context"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/linearizability"
)

func TestDefaultKeyMaxIndeterminateIsTheCheckerBound(t *testing.T) {
	if DefaultKeyMaxIndeterminate != linearizability.DefaultMaxIndeterminate {
		t.Fatalf("DefaultKeyMaxIndeterminate = %d, linearizability.DefaultMaxIndeterminate = %d: keys would outgrow the search",
			DefaultKeyMaxIndeterminate, linearizability.DefaultMaxIndeterminate)
	}
}

// TestKeyspaceRetiresAtEachBound drives one slot by hand: writes in flight
// count against the indeterminate bound, answered ones stop counting, and the
// op bound retires a key that never saw a fault.
func TestKeyspaceRetiresAtEachBound(t *testing.T) {
	ks := newKeyspace("p", 1, 5, 2)
	k1, l1 := ks.acquire(0, true)
	k2, l2 := ks.acquire(0, true)
	if k1 != "p/k0" || k2 != "p/k0" {
		t.Fatalf("first keys = %q, %q, want p/k0", k1, k2)
	}
	// Two writes in flight could both end indeterminate: a third retires.
	k3, _ := ks.acquire(0, true)
	if k3 != "p/k0-1" {
		t.Fatalf("third write went to %q, want the fresh p/k0-1", k3)
	}
	// Late returns on the retired key change nothing on its successor.
	ks.release(l1, history.OutcomeOK)
	ks.release(l2, history.OutcomeIndeterminate)
	k4, l4 := ks.acquire(0, true)
	ks.release(l4, history.OutcomeOK)
	if k4 != "p/k0-1" {
		t.Fatalf("answered write moved to %q, want p/k0-1", k4)
	}
	for range 3 {
		if k, _ := ks.acquire(0, false); k != "p/k0-1" {
			t.Fatalf("read went to %q, want p/k0-1", k)
		}
	}
	// p/k0-1 has taken its 5 operations: the next retires it.
	if k, _ := ks.acquire(0, false); k != "p/k0-2" {
		t.Fatalf("sixth op went to %q, want p/k0-2", k)
	}
	if got, want := ks.all(), []string{"p/k0", "p/k0-1", "p/k0-2"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("all() = %v, want %v", got, want)
	}
}

// TestRunKeepsEveryKeyDecidable runs a long history on few keys in which a
// third of all writes end indeterminate, as a faulty soak's do. No key may
// hold more indeterminate writes than the checker searches, and the checker
// must judge every key.
func TestRunKeepsEveryKeyDecidable(t *testing.T) {
	cfg := testConfig("n1", "n2", "n3")
	cfg.Clients, cfg.Keys, cfg.OpsPerClient = 4, 2, 1500
	f := newFakeCluster(cfg.Endpoints...)
	var n atomic.Uint64
	f.setFault(func(_ context.Context, _ string, kind history.Kind) error {
		if kind != history.KindGet && n.Add(1)%3 == 0 {
			return context.DeadlineExceeded
		}
		return nil
	})

	h, stats, err := run(t, cfg, f.dial, noPause)
	if err != nil {
		t.Fatalf("FinalRead: %v", err)
	}
	requireLedgerValid(t, h)
	if stats.Indeterminate <= uint64(2*cfg.KeyMaxIndeterminate) {
		t.Fatalf("stats = %+v: too few indeterminate writes to test the bound", stats)
	}

	indeterminate := make(map[string]int)
	keys := make(map[string]bool)
	for _, op := range h {
		keys[op.Key] = true
		if op.Kind != history.KindGet && op.Outcome == history.OutcomeIndeterminate {
			indeterminate[op.Key]++
		}
	}
	if len(keys) <= cfg.Keys {
		t.Fatalf("history used %d keys: no key was retired", len(keys))
	}
	for k, c := range indeterminate {
		if c > cfg.KeyMaxIndeterminate {
			t.Errorf("key %s holds %d indeterminate writes, bound %d", k, c, cfg.KeyMaxIndeterminate)
		}
	}
	res, err := linearizability.Checker{}.Check(h)
	if err != nil {
		t.Fatalf("linearizability could not judge the history: %v", err)
	}
	if !res.Valid {
		t.Fatalf("linearizability found anomalies: %+v", res.Anomalies)
	}
}

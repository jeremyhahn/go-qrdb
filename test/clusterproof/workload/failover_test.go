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
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
)

// halfOpenCluster models the soak's fault: a node hit by a kill, a pause or a
// partition leaves every session open to it half-open. Such a connection never
// reports itself lost; each operation on it just times out, even after the
// node has healed. Dials succeed throughout, so only failing a client over on
// an unanswered operation gets it off a dead session.
//
// The faults move through phases, each hanging a set of endpoints, and a
// phase ends once phaseOps operations were answered in it. The middle phases
// hang every endpoint but one node healed earlier, so the run can only finish
// if clients come back to a node after it heals.
type halfOpenCluster struct {
	*fakeCluster

	mu       sync.Mutex
	phases   []map[string]bool
	phase    int
	phaseOps int
	inPhase  int
	answered []map[string]int // per phase, answered operations per endpoint
}

func newHalfOpenCluster(phaseOps int, phases ...[]string) *halfOpenCluster {
	h := &halfOpenCluster{fakeCluster: newFakeCluster("n1", "n2", "n3"), phaseOps: phaseOps}
	for _, hung := range phases {
		set := make(map[string]bool)
		for _, e := range hung {
			set[e] = true
		}
		h.phases = append(h.phases, set)
		h.answered = append(h.answered, make(map[string]int))
	}
	return h
}

func (h *halfOpenCluster) dial(ctx context.Context, endpoint string) (Conn, error) {
	conn, err := h.fakeCluster.dial(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	return &halfOpenConn{Conn: conn, h: h, endpoint: endpoint}, nil
}

// hangs reports whether an operation on endpoint is hung now.
func (h *halfOpenCluster) hangs(endpoint string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.phases[h.phase][endpoint]
}

func (h *halfOpenCluster) answer(endpoint string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.answered[h.phase][endpoint]++
	h.inPhase++
	if h.inPhase >= h.phaseOps && h.phase < len(h.phases)-1 {
		h.phase++
		h.inPhase = 0
	}
}

// halfOpenConn turns dead for good the first time one of its operations hangs.
type halfOpenConn struct {
	Conn
	h        *halfOpenCluster
	endpoint string
	dead     bool
}

func (c *halfOpenConn) Put(ctx context.Context, key string, value []byte, opts ...transport.WriteOption) error {
	if c.dead || c.h.hangs(c.endpoint) {
		c.dead = true
		return context.DeadlineExceeded
	}
	if err := c.Conn.Put(ctx, key, value, opts...); err != nil {
		return err
	}
	c.h.answer(c.endpoint)
	return nil
}

// Get hangs like Put: a read on a half-open session is never answered.
func (c *halfOpenConn) Get(ctx context.Context, key string, opts ...transport.ReadOption) ([]byte, error) {
	if c.dead || c.h.hangs(c.endpoint) {
		c.dead = true
		return nil, context.DeadlineExceeded
	}
	v, err := c.Conn.Get(ctx, key, opts...)
	if err == nil || isA[*transport.KeyNotFoundError](err) {
		c.h.answer(c.endpoint)
	}
	return v, err
}

func noPause(w *Workload) { w.sleep = func(context.Context, time.Duration) {} }

func TestRunFailsOverFromHalfOpenSessions(t *testing.T) {
	cfg := testConfig("n1", "n2", "n3")
	cfg.Clients, cfg.OpsPerClient = 3, 200
	cfg.Mix = Mix{Put: 1}
	h := newHalfOpenCluster(30,
		[]string{"n1"},       // n1 dies; its client must move on
		[]string{"n2", "n3"}, // only the healed n1 answers
		[]string{"n1", "n3"}, // only n2 answers
		[]string{"n1", "n2"}, // only n3 answers
		nil,                  // every node healed
	)

	hist, _, err := run(t, cfg, h.dial, noPause)
	if err != nil {
		t.Fatalf("FinalRead: %v", err)
	}
	requireLedgerValid(t, hist)

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.phase != len(h.phases)-1 {
		t.Fatalf("run ended in fault phase %d of %d: clients stopped completing operations", h.phase, len(h.phases)-1)
	}
	for _, c := range []struct {
		endpoint string
		phase    int
	}{{"n1", 1}, {"n2", 2}, {"n3", 3}} {
		if h.answered[c.phase][c.endpoint] == 0 {
			t.Errorf("%s answered nothing in phase %d after it healed", c.endpoint, c.phase)
		}
	}
	last := make(map[int]history.Op)
	for _, op := range hist {
		if op.Client < cfg.Clients {
			last[op.Client] = op
		}
	}
	for id := range cfg.Clients {
		if op, ok := last[id]; !ok || op.Outcome != history.OutcomeOK {
			t.Errorf("client %d: last operation %+v, want ok once every node healed", id, op)
		}
	}
}

// TestRunFailsOverFromHalfOpenReads is #1637 fx1637e: a read the classifier
// calls failed because it went unanswered must still fail the client over,
// or a read-only client stays pinned to a half-open session.
func TestRunFailsOverFromHalfOpenReads(t *testing.T) {
	cfg := testConfig("n1", "n2", "n3")
	cfg.Clients, cfg.OpsPerClient = 3, 200
	cfg.Mix = Mix{Get: 1}
	h := newHalfOpenCluster(30,
		[]string{"n1"},       // n1 dies; its client must move on
		[]string{"n2", "n3"}, // only the healed n1 answers
		[]string{"n1", "n3"}, // only n2 answers
		[]string{"n1", "n2"}, // only n3 answers
		nil,                  // every node healed
	)

	hist, _, err := run(t, cfg, h.dial, noPause)
	if err != nil {
		t.Fatalf("FinalRead: %v", err)
	}
	// No ledger check: a read-only history holds no acknowledged write, so
	// the ledger calls it vacuous by construction.

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.phase != len(h.phases)-1 {
		t.Fatalf("run ended in fault phase %d of %d: read-only clients stopped completing reads", h.phase, len(h.phases)-1)
	}
	for _, c := range []struct {
		endpoint string
		phase    int
	}{{"n1", 1}, {"n2", 2}, {"n3", 3}} {
		if h.answered[c.phase][c.endpoint] == 0 {
			t.Errorf("%s answered no read in phase %d after it healed", c.endpoint, c.phase)
		}
	}
	last := make(map[int]history.Op)
	for _, op := range hist {
		if op.Client < cfg.Clients {
			last[op.Client] = op
		}
	}
	for id := range cfg.Clients {
		if op, ok := last[id]; !ok || op.Kind != history.KindGet || op.Outcome != history.OutcomeOK {
			t.Errorf("client %d: last operation %+v, want an ok read once every node healed", id, op)
		}
	}
}

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

package clusterseal

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// errElecting stands for the system shard electing a leader: a Raft read or
// proposal timing out. errFinal stands for anything else.
var (
	errElecting = errors.New("quicraft: timeout")
	errFinal    = errors.New("permission denied")
)

func electingRetryable(c *Config) {
	c.Retryable = func(err error) bool { return errors.Is(err, errElecting) }
}

func wantRecord(t *testing.T, h *harness, b Barrier, epoch uint64, sealed bool) {
	t.Helper()
	rec, ok := h.record(t, b)
	if !ok || rec.Epoch != epoch || rec.Sealed != sealed {
		t.Fatalf("record = %+v (found %v), want epoch %d sealed %v", rec, ok, epoch, sealed)
	}
}

func wantNotConfirmed(t *testing.T, err error, op string, cause error) {
	t.Helper()
	var nc *RecordNotConfirmedError
	if !errors.As(err, &nc) || nc.Op != op || !errors.Is(err, cause) {
		t.Fatalf("err = %v, want *RecordNotConfirmedError{Op: %q} wrapping %v", err, op, cause)
	}
}

// TestSealWaitsOutElectingRead is the cluster tenancy failure (#654): the
// record read times out once while the system shard elects, then succeeds.
// The seal completes, and nothing about it is unconfirmed.
func TestSealWaitsOutElectingRead(t *testing.T) {
	h := newHarness(t, electingRetryable)
	h.enf.tenants["rt1"] = true
	h.kv.getFaults = []error{errElecting}

	got, err := h.m.Seal(context.Background(), TenantBarrier("rt1"), alice)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if got.Epoch != 1 || !got.Sealed {
		t.Fatalf("returned %+v", got)
	}
	wantRecord(t, h, TenantBarrier("rt1"), 1, true)
	if calls := h.enf.callList(); !reflect.DeepEqual(calls, []string{"tenant/rt1"}) {
		t.Fatalf("enforcement = %v", calls)
	}
}

func TestSealWaitsOutElectingReadOverExistingRecord(t *testing.T) {
	h := newHarness(t, electingRetryable)
	h.enf.tenants["rt1"] = true
	h.putRecord(t, Record{Barrier: TenantBarrier("rt1"), Epoch: 2, Node: peerB})
	h.kv.getFaults = []error{errElecting, errElecting, errElecting}

	if _, err := h.m.Seal(context.Background(), TenantBarrier("rt1"), alice); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	wantRecord(t, h, TenantBarrier("rt1"), 3, true)
	if h.kv.gets != 4 {
		t.Fatalf("gets = %d, want 4", h.kv.gets)
	}
}

// TestSealWaitsOutElectingWriteThatCommitted: the proposal commits but its
// caller hears a timeout. The retry finds its own record and adopts it rather
// than reporting a conflict.
func TestSealWaitsOutElectingWriteThatCommitted(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run("existing="+strconv.FormatBool(existing), func(t *testing.T) {
			h := newHarness(t, electingRetryable)
			h.enf.tenants["rt1"] = true
			want := uint64(1)
			if existing {
				h.putRecord(t, Record{Barrier: TenantBarrier("rt1"), Epoch: 2, Node: peerB})
				want = 3
			}
			h.kv.writeFaults = []error{errElecting}
			h.kv.faultCommits = true

			got, err := h.m.Seal(context.Background(), TenantBarrier("rt1"), alice)
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			if got.Epoch != want || !got.Sealed || got.Principal != alice.Name {
				t.Fatalf("returned %+v", got)
			}
			wantRecord(t, h, TenantBarrier("rt1"), want, true)
			if calls := h.enf.callList(); !reflect.DeepEqual(calls, []string{"tenant/rt1"}) {
				t.Fatalf("enforcement = %v", calls)
			}
		})
	}
}

func TestSealWaitsOutElectingWriteThatDidNotCommit(t *testing.T) {
	h := newHarness(t, electingRetryable)
	h.enf.tenants["rt1"] = true
	h.kv.writeFaults = []error{errElecting, errElecting}

	if _, err := h.m.Seal(context.Background(), TenantBarrier("rt1"), alice); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	wantRecord(t, h, TenantBarrier("rt1"), 1, true)
	if h.kv.writes != 3 {
		t.Fatalf("writes = %d, want 3", h.kv.writes)
	}
}

// TestSealFinalErrorIsNotRetried: only the transient errors wait.
func TestSealFinalErrorIsNotRetried(t *testing.T) {
	h := newHarness(t, electingRetryable)
	h.enf.tenants["rt1"] = true
	h.kv.getFaults = []error{errFinal}
	start := h.clock.Now()

	_, err := h.m.Seal(context.Background(), TenantBarrier("rt1"), alice)
	wantNotConfirmed(t, err, "seal", errFinal)
	if h.kv.gets != 1 || !h.clock.Now().Equal(start) {
		t.Fatalf("gets = %d, waited %v; a final error was retried", h.kv.gets, h.clock.Now().Sub(start))
	}
	if h.enf.TenantUnsealed("rt1") {
		t.Fatal("the receiving node is not sealed")
	}
}

// TestSealElectionOutlastsBudget: an election longer than SettleBudget ends
// the wait with the typed error, and the receiving node is sealed regardless.
func TestSealElectionOutlastsBudget(t *testing.T) {
	h := newHarness(t, electingRetryable)
	h.enf.tenants["rt1"] = true
	h.kv.readErr = errElecting
	start := h.clock.Now()

	_, err := h.m.Seal(context.Background(), TenantBarrier("rt1"), alice)
	wantNotConfirmed(t, err, "seal", errElecting)
	if waited := h.clock.Now().Sub(start); waited < SettleBudget || waited > SettleBudget+time.Second {
		t.Fatalf("waited %v, want the %v budget", waited, SettleBudget)
	}
	if h.enf.TenantUnsealed("rt1") {
		t.Fatal("the receiving node is not sealed")
	}
}

// TestSealBudgetSpansTheRequest: a read and a write that each wait out part
// of an election share one budget rather than taking one each.
func TestSealBudgetSpansTheRequest(t *testing.T) {
	h := newHarness(t, electingRetryable)
	h.enf.tenants["rt1"] = true
	start := h.clock.Now()
	// Reads fail for two thirds of the budget, then writes for as long
	// again: each fits a budget of its own, together they outlast one.
	steps := int(SettleBudget/settlePoll) * 2 / 3
	for range steps {
		h.kv.getFaults = append(h.kv.getFaults, errElecting)
		h.kv.writeFaults = append(h.kv.writeFaults, errElecting)
	}

	_, err := h.m.Seal(context.Background(), TenantBarrier("rt1"), alice)
	wantNotConfirmed(t, err, "seal", errElecting)
	if waited := h.clock.Now().Sub(start); waited > SettleBudget+time.Second {
		t.Fatalf("waited %v across the request, budget %v", waited, SettleBudget)
	}
}

func TestSealHonoursCancellationWhileElecting(t *testing.T) {
	h := newHarness(t, electingRetryable)
	h.enf.tenants["rt1"] = true
	h.kv.readErr = errElecting
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := h.m.Seal(ctx, TenantBarrier("rt1"), alice)
	wantNotConfirmed(t, err, "seal", errElecting)
	if h.kv.gets != 1 {
		t.Fatalf("gets = %d after cancellation, want 1", h.kv.gets)
	}
}

// TestSealWithoutRetryableIsSingleShot: a monitor told nothing is transient
// treats every error as final.
func TestSealWithoutRetryableIsSingleShot(t *testing.T) {
	h := newHarness(t)
	h.enf.tenants["rt1"] = true
	h.kv.getFaults = []error{errElecting}

	_, err := h.m.Seal(context.Background(), TenantBarrier("rt1"), alice)
	wantNotConfirmed(t, err, "seal", errElecting)
	if h.kv.gets != 1 {
		t.Fatalf("gets = %d, want 1", h.kv.gets)
	}
}

func TestClearWaitsOutElectingRead(t *testing.T) {
	h := newHarness(t, electingRetryable)
	h.putRecord(t, Record{Barrier: TenantBarrier("rt1"), Epoch: 1, Sealed: true, Node: peerB})
	h.kv.getFaults = []error{errElecting, errElecting}

	got, err := h.m.Clear(context.Background(), TenantBarrier("rt1"), alice)
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if got.Epoch != 2 || got.Sealed {
		t.Fatalf("returned %+v", got)
	}
	wantRecord(t, h, TenantBarrier("rt1"), 2, false)
}

func TestClearWaitsOutElectingWriteThatCommitted(t *testing.T) {
	h := newHarness(t, electingRetryable)
	h.putRecord(t, Record{Barrier: TenantBarrier("rt1"), Epoch: 1, Sealed: true, Node: peerB})
	h.kv.writeFaults = []error{errElecting}
	h.kv.faultCommits = true

	got, err := h.m.Clear(context.Background(), TenantBarrier("rt1"), alice)
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if got.Epoch != 2 || got.Sealed {
		t.Fatalf("returned %+v", got)
	}
	wantRecord(t, h, TenantBarrier("rt1"), 2, false)
}

func TestClearFinalErrorIsNotRetried(t *testing.T) {
	h := newHarness(t, electingRetryable)
	h.putRecord(t, Record{Barrier: TenantBarrier("rt1"), Epoch: 1, Sealed: true, Node: peerB})
	h.kv.writeFaults = []error{errFinal}

	_, err := h.m.Clear(context.Background(), TenantBarrier("rt1"), alice)
	wantNotConfirmed(t, err, "clear", errFinal)
	if h.kv.writes != 1 {
		t.Fatalf("writes = %d, want 1", h.kv.writes)
	}
	wantRecord(t, h, TenantBarrier("rt1"), 1, true)
}

func TestClearElectionOutlastsBudget(t *testing.T) {
	h := newHarness(t, electingRetryable)
	h.putRecord(t, Record{Barrier: TenantBarrier("rt1"), Epoch: 1, Sealed: true, Node: peerB})
	h.kv.readErr = errElecting
	start := h.clock.Now()

	_, err := h.m.Clear(context.Background(), TenantBarrier("rt1"), alice)
	wantNotConfirmed(t, err, "clear", errElecting)
	if waited := h.clock.Now().Sub(start); waited < SettleBudget || waited > SettleBudget+time.Second {
		t.Fatalf("waited %v, want the %v budget", waited, SettleBudget)
	}
}

// TestNodeSealAckWaitsOutElection: the node-seal acknowledgement is retried
// within AckTimeout, so the peers waiting on it are not left to time out.
func TestNodeSealAckWaitsOutElection(t *testing.T) {
	h := newHarness(t, electingRetryable)
	h.members = []string{self}
	h.kv.putFaults = []error{errElecting, errElecting}

	if _, err := h.m.Seal(context.Background(), NodeBarrier(), ops); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if v, ok := h.kv.get(ackKey(self)); !ok || string(v) != "1" {
		t.Fatalf("ack = %q (found %v), want epoch 1", v, ok)
	}
	if h.kv.puts != 3 {
		t.Fatalf("puts = %d, want 3", h.kv.puts)
	}
}

func TestNodeSealAckFinalErrorIsNotRetried(t *testing.T) {
	h := newHarness(t, electingRetryable)
	h.members = []string{self}
	h.kv.putFaults = []error{errFinal}

	if _, err := h.m.Seal(context.Background(), NodeBarrier(), ops); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, ok := h.kv.get(ackKey(self)); ok || h.kv.puts != 1 {
		t.Fatalf("ack written %v after %d puts; a final error was retried", ok, h.kv.puts)
	}
}

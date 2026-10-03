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
	"strings"
	"testing"
	"time"
)

func TestErrorMessagesNameTheBarrierAndUnwrap(t *testing.T) {
	cause := errors.New("cause")
	tests := []struct {
		err      error
		contains string
		unwraps  bool
	}{
		{&InvalidBarrierError{Name: "x/y"}, `"x/y"`, false},
		{&CorruptRecordError{Barrier: TenantBarrier("acme"), Err: cause}, `"tenant/acme"`, true},
		{&CorruptRecordError{}, "corrupt seal record", false},
		{&SealEpochConflictError{Barrier: NodeBarrier()}, `"node"`, false},
		{&RecordNotConfirmedError{Barrier: NodeBarrier(), Op: "seal", Err: cause}, "seal record", true},
		{&SealedError{Barrier: DatabaseBarrier("acme", "orders")}, `"database/acme/orders"`, false},
		{&SealStateUnconfirmedError{}, "cannot confirm", false},
		{&ConfigError{Field: "Lease", Reason: "bad"}, "Lease", false},
		{&AckError{Err: cause}, "acknowledgement", true},
	}
	for _, tc := range tests {
		if !strings.Contains(tc.err.Error(), tc.contains) {
			t.Errorf("%T message %q does not contain %q", tc.err, tc.err.Error(), tc.contains)
		}
		if got := errors.Is(tc.err, cause); got != tc.unwraps {
			t.Errorf("%T unwraps to the cause = %v, want %v", tc.err, got, tc.unwraps)
		}
	}
}

func TestBarrierPartsOfOtherKinds(t *testing.T) {
	if NodeBarrier().Tenant() != "" || NodeBarrier().Database() != "" {
		t.Fatal("the node barrier has a tenant or database")
	}
	if TenantBarrier("acme").Database() != "" {
		t.Fatal("a tenant barrier has a database")
	}
	if Barrier("bogus").Tenant() != "" || Barrier("bogus").Kind() != KindInvalid {
		t.Fatal("an invalid barrier parsed")
	}
}

func TestRealClock(t *testing.T) {
	c := realClock{}
	before := time.Now()
	if c.Now().Before(before) {
		t.Fatal("realClock.Now went backwards")
	}
	select {
	case <-c.After(time.Millisecond):
	case <-time.After(5 * time.Second):
		t.Fatal("realClock.After never fired")
	}
}

func TestMonitorIsSealedMatchesRefusing(t *testing.T) {
	h := newHarness(t)
	if !h.m.IsSealed() {
		t.Fatal("IsSealed false before the first read")
	}
	h.poll(t)
	if h.m.IsSealed() {
		t.Fatal("IsSealed true while serving")
	}
}

func TestSealAndClearRefuseInvalidBarrier(t *testing.T) {
	h := newHarness(t)
	h.poll(t)
	var bad *InvalidBarrierError
	if _, err := h.m.Seal(context.Background(), Barrier("tenant/"), alice); !errors.As(err, &bad) {
		t.Fatalf("Seal err = %v", err)
	}
	if _, err := h.m.Clear(context.Background(), Barrier("x"), alice); !errors.As(err, &bad) {
		t.Fatalf("Clear err = %v", err)
	}
	if err := h.m.enforceLocked(context.Background(), Barrier("x"), 1); !errors.As(err, &bad) {
		t.Fatalf("enforce err = %v", err)
	}
}

// TestSealWithUnreadableRecordStillSealsLocally: the current record cannot be
// read, so its epoch is unknown; the node seals itself regardless.
func TestSealWithUnreadableRecordStillSealsLocally(t *testing.T) {
	h := newHarness(t)
	h.enf.tenants["acme"] = true
	h.poll(t)
	h.kv.readErr = errors.New("no quorum")
	_, err := h.m.Seal(context.Background(), TenantBarrier("acme"), alice)
	var nc *RecordNotConfirmedError
	if !errors.As(err, &nc) || nc.Op != "seal" {
		t.Fatalf("err = %v", err)
	}
	if h.enf.TenantUnsealed("acme") {
		t.Fatal("tenant left unsealed")
	}
}

func TestSealLocalFailureIsReportedWithTheUnconfirmedRecord(t *testing.T) {
	h := newHarness(t)
	h.poll(t)
	h.kv.writeErr = errors.New("no quorum")
	h.enf.setErr(errors.New("close failed"))
	_, err := h.m.Seal(context.Background(), TenantBarrier("acme"), alice)
	var nc *RecordNotConfirmedError
	if !errors.As(err, &nc) {
		t.Fatalf("err = %v", err)
	}
}

func TestSealReportsLocalEnforcementFailure(t *testing.T) {
	h := newHarness(t)
	h.poll(t)
	boom := errors.New("close failed")
	h.enf.setErr(boom)
	if _, err := h.m.Seal(context.Background(), TenantBarrier("acme"), alice); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the enforcement failure", err)
	}
	if rec, ok := h.record(t, TenantBarrier("acme")); !ok || !rec.Sealed {
		t.Fatal("record not written")
	}
	// The next read retries the enforcement.
	h.enf.setErr(nil)
	h.poll(t)
	if got := h.enf.callList(); len(got) != 2 {
		t.Fatalf("enforcement = %v, want a retry", got)
	}
}

func TestClearWithUnreadableRecord(t *testing.T) {
	h := newHarness(t)
	h.poll(t)
	h.kv.readErr = errors.New("no quorum")
	_, err := h.m.Clear(context.Background(), TenantBarrier("acme"), alice)
	var nc *RecordNotConfirmedError
	if !errors.As(err, &nc) || nc.Op != "clear" {
		t.Fatalf("err = %v", err)
	}
}

func TestClearRacingAClearSucceeds(t *testing.T) {
	h := newHarness(t)
	h.putRecord(t, Record{Barrier: TenantBarrier("acme"), Epoch: 1, Sealed: true})
	h.poll(t)
	h.kv.beforeWrite = func() {
		h.putRecord(t, Record{Barrier: TenantBarrier("acme"), Epoch: 2, Sealed: false, Node: peerB})
	}
	rec, err := h.m.Clear(context.Background(), TenantBarrier("acme"), alice)
	if err != nil || rec.Node != peerB || rec.Sealed {
		t.Fatalf("Clear = %+v, %v; want the winner's clear", rec, err)
	}
	if h.m.Refuses(TenantBarrier("acme")) {
		t.Fatal("gate closed after a winning clear")
	}
}

func TestPollIgnoresRecordsUnderInvalidKeysAndMismatchedBarriers(t *testing.T) {
	h := newHarness(t)
	h.kv.set(RecordsPrefix+"not-a-barrier", []byte(`{}`))
	raw, err := encodeRecord(Record{Barrier: TenantBarrier("other"), Epoch: 1, Sealed: true})
	if err != nil {
		t.Fatal(err)
	}
	h.kv.set(recordKey(TenantBarrier("acme")), raw) // stored under the wrong key
	h.poll(t)
	if !h.m.Refuses(TenantBarrier("acme")) {
		t.Fatal("a record under the wrong key was trusted")
	}
	if h.m.Refuses(TenantBarrier("other")) {
		t.Fatal("a record under the wrong key sealed the barrier it names")
	}
}

func TestAckWaitWithUnreadableMembersOrAcksRunsToTimeout(t *testing.T) {
	for name, mutate := range map[string]func(*harness){
		"members unavailable": func(h *harness) {
			h.m.cfg.Members = func(context.Context) ([]string, error) { return nil, errors.New("no membership") }
		},
		"acks unreadable": func(h *harness) {
			h.kv.onScan = func() {
				h.kv.mu.Lock()
				h.kv.readErr = errors.New("no quorum")
				h.kv.mu.Unlock()
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.poll(t)
			h.kv.writeErr = nil
			mutate(h)
			start := h.clock.Now()
			h.m.awaitAcks(context.Background(), 1)
			if h.clock.Now().Sub(start) < h.m.cfg.AckTimeout {
				t.Fatal("the wait ended before the timeout without every ack")
			}
		})
	}
}

func TestAckWriteFailureStillWaitsAndProceeds(t *testing.T) {
	h := newHarness(t)
	h.poll(t)
	h.members = []string{self}
	h.kv.writeErr = errors.New("no quorum")
	h.m.awaitAcks(context.Background(), 1) // returns: this node is the only member
}

func TestAckWaitStopsOnCancel(t *testing.T) {
	h := newHarness(t)
	h.poll(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.m.awaitAcks(ctx, 1)
	if h.clock.Now().Sub(newFakeClock().Now()) >= h.m.cfg.AckTimeout {
		t.Fatal("a cancelled wait ran to the timeout")
	}
}

func TestStatusOfAnUnknownKindIsSealed(t *testing.T) {
	h := newHarness(t)
	h.poll(t)
	if got := h.m.Status(Barrier("bogus")).Local; got != LocalSealed {
		t.Fatalf("local = %q", got)
	}
}

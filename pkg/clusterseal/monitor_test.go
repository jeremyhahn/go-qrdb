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

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

const self = "192.0.2.1:5000"

var (
	peerB = "192.0.2.2:5000"
	peerC = "192.0.2.3:5000"
	alice = Principal{Name: "alice", Tenant: "acme"}
	ops   = Principal{Name: "ops", Tenant: "system"}
)

type harness struct {
	kv      *memKV
	enf     *fakeEnforcer
	gate    *fakeGate
	clock   *fakeClock
	members []string
	m       *Monitor
}

func newHarness(t *testing.T, mutate ...func(*Config)) *harness {
	t.Helper()
	h := &harness{
		kv:      newMemKV(),
		enf:     newFakeEnforcer(),
		gate:    &fakeGate{activated: true},
		clock:   newFakeClock(),
		members: []string{self, peerB, peerC},
	}
	cfg := Config{
		KV:           h.kv,
		Gate:         h.gate,
		Enforcer:     h.enf,
		Self:         self,
		Members:      func(context.Context) ([]string, error) { return h.members, nil },
		Lease:        15 * time.Second,
		PollInterval: time.Second,
		AckTimeout:   5 * time.Second,
		Clock:        h.clock,
	}
	for _, f := range mutate {
		f(&cfg)
	}
	m, err := NewMonitor(cfg)
	if err != nil {
		t.Fatalf("NewMonitor: %v", err)
	}
	h.m = m
	return h
}

func (h *harness) poll(t *testing.T) {
	t.Helper()
	h.m.poll(context.Background())
}

func (h *harness) putRecord(t *testing.T, rec Record) {
	t.Helper()
	raw, err := encodeRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	h.kv.set(recordKey(rec.Barrier), raw)
}

func (h *harness) record(t *testing.T, b Barrier) (Record, bool) {
	t.Helper()
	raw, ok := h.kv.get(recordKey(b))
	if !ok {
		return Record{}, false
	}
	rec, err := decodeRecord(raw)
	if err != nil {
		t.Fatal(err)
	}
	return rec, true
}

func TestNewMonitorValidatesConfig(t *testing.T) {
	ok := func() Config {
		return Config{
			KV: newMemKV(), Enforcer: newFakeEnforcer(), Self: self,
			Members: func(context.Context) ([]string, error) { return nil, nil },
		}
	}
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"no kv", func(c *Config) { c.KV = nil }},
		{"no enforcer", func(c *Config) { c.Enforcer = nil }},
		{"no self", func(c *Config) { c.Self = "" }},
		{"no members", func(c *Config) { c.Members = nil }},
		{"negative lease", func(c *Config) { c.Lease = -time.Second }},
		{"negative ack timeout", func(c *Config) { c.AckTimeout = -time.Second }},
		{"poll too slow for lease", func(c *Config) { c.Lease = 3 * time.Second; c.PollInterval = 2 * time.Second }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := ok()
			tc.mutate(&cfg)
			_, err := NewMonitor(cfg)
			var cerr *ConfigError
			if !errors.As(err, &cerr) {
				t.Fatalf("err = %v, want *ConfigError", err)
			}
		})
	}
	m, err := NewMonitor(ok())
	if err != nil {
		t.Fatal(err)
	}
	if m.cfg.Lease != DefaultLease || m.cfg.PollInterval != DefaultPollInterval || m.cfg.AckTimeout != DefaultAckTimeout {
		t.Fatalf("defaults = %v/%v/%v", m.cfg.Lease, m.cfg.PollInterval, m.cfg.AckTimeout)
	}
	if m.Streams() == nil {
		t.Fatal("Streams() is nil")
	}
}

// TestInactiveIsNodeLocal: until the cluster activates cluster-seal, nothing is
// written, enforced or refused, and status says so. A monitor with a gate
// still reads the records (TestActivationServesAtOnce); one without a gate
// can never activate and reads nothing.
func TestInactiveIsNodeLocal(t *testing.T) {
	for name, noGate := range map[string]bool{"no gate": true, "not activated": false} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, func(c *Config) {
				if noGate {
					c.Gate = nil
				}
			})
			h.gate.set(false)
			if h.m.Active() {
				t.Fatal("Active() with cluster-seal not activated")
			}
			if h.m.Refusing() || h.m.Refuses(TenantBarrier("acme")) {
				t.Fatal("an inactive monitor refused")
			}
			if _, err := h.m.Seal(context.Background(), TenantBarrier("acme"), alice); !errors.Is(err, ErrInactive) {
				t.Fatalf("Seal err = %v, want ErrInactive", err)
			}
			if _, err := h.m.Clear(context.Background(), NodeBarrier(), ops); !errors.Is(err, ErrInactive) {
				t.Fatalf("Clear err = %v, want ErrInactive", err)
			}
			h.poll(t)
			if wantScans := map[bool]int{true: 0, false: 1}[noGate]; h.kv.scans != wantScans {
				t.Fatalf("an inactive monitor read the records %d times, want %d", h.kv.scans, wantScans)
			}
			if h.m.Refusing() || h.m.Refuses(TenantBarrier("acme")) {
				t.Fatal("an inactive monitor refused after a read")
			}
			if st := h.m.Status(NodeBarrier()); st.Active || st.Local != "" || st.Record != (Record{}) {
				t.Fatalf("Status = %+v, want inactive and empty", st)
			}
			if len(h.kv.snapshot()) != 0 || len(h.enf.callList()) != 0 {
				t.Fatal("an inactive monitor wrote or enforced")
			}
		})
	}
}

// TestActivationServesAtOnce: a monitor reads the records before cluster-seal
// is activated, without enforcing them, so when the feature turns on its
// lease is already fresh and nothing is refused while it waits for its next
// read. No record can exist before activation, so the read is accurate.
func TestActivationServesAtOnce(t *testing.T) {
	h := newHarness(t)
	h.gate.set(false)
	h.poll(t)
	h.gate.set(true)
	if h.m.Refusing() {
		t.Fatal("the node refused requests the moment cluster-seal activated")
	}
	if h.m.Refuses(TenantBarrier("acme")) || h.m.Refuses(DatabaseBarrier("acme", "orders")) {
		t.Fatal("a barrier with no record was refused the moment cluster-seal activated")
	}
	if got := h.m.Status(NodeBarrier()).Local; got != LocalServing {
		t.Fatalf("local = %q, want serving", got)
	}
}

// TestInactiveReadEnforcesNothing: a record the inactive monitor reads is
// published, not acted on, and a read that fails before activation ends no
// stream however long it lasts. The first read after activation is the one
// that applies the records, as the first read after a boot.
func TestInactiveReadEnforcesNothing(t *testing.T) {
	h := newHarness(t)
	h.gate.set(false)
	h.putRecord(t, Record{Barrier: TenantBarrier("acme"), Epoch: 1, Sealed: true})
	h.poll(t)
	if calls := h.enf.callList(); len(calls) != 0 {
		t.Fatalf("an inactive monitor enforced: %v", calls)
	}
	sub := h.m.Streams().Subscribe("acme", "")
	h.kv.mu.Lock()
	h.kv.readErr = errors.New("unreadable")
	h.kv.mu.Unlock()
	h.clock.Advance(time.Minute)
	h.poll(t)
	select {
	case <-sub.Done():
		t.Fatal("an inactive monitor ended a stream when its read failed")
	default:
	}

	h.kv.mu.Lock()
	h.kv.readErr = nil
	h.kv.mu.Unlock()
	h.gate.set(true)
	h.poll(t)
	if !h.m.Refuses(TenantBarrier("acme")) {
		t.Fatal("a sealed tenant record was not refused once active")
	}
	if calls := h.enf.callList(); len(calls) == 0 {
		t.Fatal("the first read after activation did not enforce the sealed record")
	}
}

// TestSealAfterActivationIsEnforced: the first-read-after-boot exemption
// covers only the node's first read after it starts. A node that has been
// reading the records since before activation unsealed before any seal, so
// a node seal it reads after activation is enforced, not held.
func TestSealAfterActivationIsEnforced(t *testing.T) {
	h := newHarness(t)
	h.gate.set(false)
	h.poll(t)
	h.gate.set(true)
	h.putRecord(t, Record{Barrier: NodeBarrier(), Epoch: 1, Sealed: true, Node: peerB})
	h.poll(t)
	if got := h.enf.callList(); !reflect.DeepEqual(got, []string{"node"}) {
		t.Fatalf("a node seal read after activation was not enforced: %v", got)
	}
}

// TestReadBeforeActivationsLoadedIsNotTheBootRead: a read made before the
// gate has read the cluster's activations cannot tell whether records may
// exist, so the boot exemption still applies to the first read made once it
// knows: a node restarted under a sealed node record comes up held.
func TestReadBeforeActivationsLoadedIsNotTheBootRead(t *testing.T) {
	h := newHarness(t)
	h.gate.setLoaded(false)
	h.putRecord(t, Record{Barrier: NodeBarrier(), Epoch: 3, Sealed: true, Node: peerB})
	h.poll(t)
	h.gate.setLoaded(true)
	h.poll(t)
	if got := h.enf.callList(); len(got) != 0 {
		t.Fatalf("a node restarted under a sealed record sealed itself: %v", got)
	}
	if got := h.m.Status(NodeBarrier()).Local; got != LocalHeld {
		t.Fatalf("local = %q, want held", got)
	}
}

// TestActivationsLoadedDuringReadIsNotTheBootRead: the gate loads the
// activations on its own goroutine, so it can finish while a read begun
// before it is in flight. That read saw the feature inactive and applied
// nothing; it must not spend the boot exemption, or the next read treats the
// sealed node record as a seal this boot has not applied and wipes the key
// the operator just unsealed instead of holding.
func TestActivationsLoadedDuringReadIsNotTheBootRead(t *testing.T) {
	h := newHarness(t)
	h.gate.setLoaded(false)
	h.putRecord(t, Record{Barrier: NodeBarrier(), Epoch: 3, Sealed: true, Node: peerB})
	h.kv.onScan = func() { h.gate.setLoaded(true) }
	h.poll(t)
	h.kv.onScan = nil
	h.poll(t)
	if got := h.enf.callList(); len(got) != 0 {
		t.Fatalf("a node restarted under a sealed record sealed itself: %v", got)
	}
	if got := h.m.Status(NodeBarrier()).Local; got != LocalHeld {
		t.Fatalf("local = %q, want held", got)
	}
}

// TestRestartRefusesUntilFirstRead: a node holds no lease when it starts, so it
// refuses until it has read the records once.
func TestRestartRefusesUntilFirstRead(t *testing.T) {
	h := newHarness(t)
	if !h.m.Refusing() {
		t.Fatal("served before the first read")
	}
	if got := h.m.Status(NodeBarrier()).Local; got != LocalUnconfirmed {
		t.Fatalf("local = %q, want unconfirmed", got)
	}
	h.poll(t)
	if h.m.Refusing() {
		t.Fatal("still refusing after a successful read")
	}
	if got := h.m.Status(NodeBarrier()).Local; got != LocalServing {
		t.Fatalf("local = %q, want serving", got)
	}
}

func TestSealTenantWritesRecordAndEnforcesLocally(t *testing.T) {
	h := newHarness(t)
	h.enf.tenants["acme"] = true
	h.poll(t)
	acmeSub := h.m.Streams().Subscribe("acme", "orders")
	otherSub := h.m.Streams().Subscribe("other", "orders")

	rec, err := h.m.Seal(context.Background(), TenantBarrier("acme"), alice)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	want := Record{Barrier: TenantBarrier("acme"), Epoch: 1, Sealed: true,
		Principal: "alice", PrincipalTenant: "acme", Node: self, At: h.clock.Now().UTC()}
	if rec != want {
		t.Fatalf("record = %+v, want %+v", rec, want)
	}
	if stored, _ := h.record(t, TenantBarrier("acme")); stored != want {
		t.Fatalf("stored = %+v, want %+v", stored, want)
	}
	if got := h.enf.callList(); !reflect.DeepEqual(got, []string{"tenant/acme"}) {
		t.Fatalf("enforcement = %v", got)
	}
	if !ended(acmeSub) || ended(otherSub) {
		t.Fatal("the tenant's streams were not ended, or another tenant's were")
	}
	var sealed *SealedError
	if !errors.As(acmeSub.Cause(), &sealed) || sealed.Barrier != TenantBarrier("acme") {
		t.Fatalf("cause = %v, want SealedError for the tenant", acmeSub.Cause())
	}
	if !h.m.Refuses(TenantBarrier("acme")) || h.m.Refuses(TenantBarrier("other")) {
		t.Fatal("tenant gate not closed, or another tenant's closed")
	}
	if h.m.Refusing() {
		t.Fatal("a tenant seal closed the node gate")
	}

	// A second seal is idempotent: same epoch, no second enforcement.
	again, err := h.m.Seal(context.Background(), TenantBarrier("acme"), alice)
	if err != nil || again.Epoch != 1 {
		t.Fatalf("repeat Seal = %+v, %v", again, err)
	}
	h.poll(t)
	if got := h.enf.callList(); len(got) != 1 {
		t.Fatalf("enforced %d times, want once", len(got))
	}
}

func TestSealDatabaseEndsOnlyItsStreams(t *testing.T) {
	h := newHarness(t)
	h.enf.databases["acme/orders"] = true
	h.poll(t)
	mine := h.m.Streams().Subscribe("acme", "orders")
	sibling := h.m.Streams().Subscribe("acme", "billing")
	if _, err := h.m.Seal(context.Background(), DatabaseBarrier("acme", "orders"), alice); err != nil {
		t.Fatal(err)
	}
	if got := h.enf.callList(); !reflect.DeepEqual(got, []string{"database/acme/orders"}) {
		t.Fatalf("enforcement = %v", got)
	}
	if !ended(mine) || ended(sibling) {
		t.Fatal("database seal ended the wrong streams")
	}
	if st := h.m.Status(DatabaseBarrier("acme", "orders")); !st.Record.Sealed || st.Local != LocalSealed {
		t.Fatalf("status = %+v", st)
	}
}

// TestRemoteSealIsEnforcedOncePerEpoch: a record written through another node
// is enforced on the next poll, exactly once.
func TestRemoteSealIsEnforcedOncePerEpoch(t *testing.T) {
	h := newHarness(t)
	h.enf.tenants["acme"] = true
	h.poll(t)
	h.putRecord(t, Record{Barrier: TenantBarrier("acme"), Epoch: 4, Sealed: true, Node: peerB})
	h.poll(t)
	h.poll(t)
	if got := h.enf.callList(); !reflect.DeepEqual(got, []string{"tenant/acme"}) {
		t.Fatalf("enforcement = %v, want exactly one", got)
	}
	// A clear followed by a newer seal is enforced again.
	h.putRecord(t, Record{Barrier: TenantBarrier("acme"), Epoch: 5, Sealed: false, Node: peerB})
	h.poll(t)
	if h.m.Refuses(TenantBarrier("acme")) {
		t.Fatal("still refusing after the clear record")
	}
	h.putRecord(t, Record{Barrier: TenantBarrier("acme"), Epoch: 6, Sealed: true, Node: peerC})
	h.poll(t)
	if got := h.enf.callList(); len(got) != 2 {
		t.Fatalf("enforcement = %v, want two", got)
	}
}

func TestClearWritesNextEpochAndOpensTheGate(t *testing.T) {
	h := newHarness(t)
	h.enf.tenants["acme"] = true
	h.poll(t)
	if _, err := h.m.Seal(context.Background(), TenantBarrier("acme"), alice); err != nil {
		t.Fatal(err)
	}
	h.enf.tenants["acme"] = true // the admin unsealed this node
	rec, err := h.m.Clear(context.Background(), TenantBarrier("acme"), alice)
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if rec.Epoch != 2 || rec.Sealed {
		t.Fatalf("clear record = %+v", rec)
	}
	if h.m.Refuses(TenantBarrier("acme")) {
		t.Fatal("gate still closed after clear")
	}
	h.poll(t)
	if got := h.enf.callList(); len(got) != 1 {
		t.Fatalf("clear triggered enforcement: %v", got)
	}
	// Clearing a clear record writes nothing.
	before := h.kv.snapshot()
	again, err := h.m.Clear(context.Background(), TenantBarrier("acme"), alice)
	if err != nil || again.Epoch != 2 {
		t.Fatalf("repeat Clear = %+v, %v", again, err)
	}
	if !reflect.DeepEqual(before, h.kv.snapshot()) {
		t.Fatal("a clear of a clear record wrote")
	}
	// Clearing a barrier never sealed writes nothing either.
	if rec, err := h.m.Clear(context.Background(), TenantBarrier("fresh"), alice); err != nil || rec.Epoch != 0 {
		t.Fatalf("Clear of a never-sealed barrier = %+v, %v", rec, err)
	}
}

func TestGuardedWriteRaces(t *testing.T) {
	t.Run("seal racing a seal succeeds idempotently", func(t *testing.T) {
		h := newHarness(t)
		h.enf.tenants["acme"] = true
		h.poll(t)
		h.kv.beforeWrite = func() {
			h.putRecord(t, Record{Barrier: TenantBarrier("acme"), Epoch: 1, Sealed: true, Node: peerB})
		}
		rec, err := h.m.Seal(context.Background(), TenantBarrier("acme"), alice)
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		if rec.Node != peerB || rec.Epoch != 1 {
			t.Fatalf("record = %+v, want the winner's", rec)
		}
		if got := h.enf.callList(); !reflect.DeepEqual(got, []string{"tenant/acme"}) {
			t.Fatalf("enforcement = %v", got)
		}
	})
	t.Run("clear racing a seal is refused", func(t *testing.T) {
		h := newHarness(t)
		h.enf.tenants["acme"] = true
		h.putRecord(t, Record{Barrier: TenantBarrier("acme"), Epoch: 1, Sealed: true, Node: peerB})
		h.poll(t)
		h.enf.tenants["acme"] = true
		h.kv.beforeWrite = func() {
			h.putRecord(t, Record{Barrier: TenantBarrier("acme"), Epoch: 3, Sealed: true, Node: peerC})
		}
		_, err := h.m.Clear(context.Background(), TenantBarrier("acme"), alice)
		var conflict *SealEpochConflictError
		if !errors.As(err, &conflict) || conflict.Barrier != TenantBarrier("acme") {
			t.Fatalf("err = %v, want *SealEpochConflictError", err)
		}
	})
	t.Run("seal racing a clear is refused", func(t *testing.T) {
		h := newHarness(t)
		h.poll(t)
		h.kv.beforeWrite = func() {
			h.putRecord(t, Record{Barrier: TenantBarrier("acme"), Epoch: 2, Sealed: false, Node: peerB})
		}
		_, err := h.m.Seal(context.Background(), TenantBarrier("acme"), alice)
		var conflict *SealEpochConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("err = %v, want *SealEpochConflictError", err)
		}
	})
}

// TestSealWithUnconfirmedRecordStillSealsLocally: sealing is the safe
// direction, so a record that cannot be written does not stop this node
// sealing itself.
func TestSealWithUnconfirmedRecordStillSealsLocally(t *testing.T) {
	h := newHarness(t)
	h.enf.tenants["acme"] = true
	h.poll(t)
	boom := errors.New("no quorum")
	h.kv.writeErr = boom
	_, err := h.m.Seal(context.Background(), TenantBarrier("acme"), alice)
	var nc *RecordNotConfirmedError
	if !errors.As(err, &nc) || !errors.Is(err, boom) {
		t.Fatalf("err = %v, want *RecordNotConfirmedError wrapping the cause", err)
	}
	if got := h.enf.callList(); !reflect.DeepEqual(got, []string{"tenant/acme"}) {
		t.Fatalf("enforcement = %v, want the local seal", got)
	}
	if h.enf.TenantUnsealed("acme") {
		t.Fatal("tenant left unsealed")
	}
}

func TestClearWriteFailureIsReported(t *testing.T) {
	h := newHarness(t)
	h.putRecord(t, Record{Barrier: TenantBarrier("acme"), Epoch: 1, Sealed: true, Node: peerB})
	h.poll(t)
	h.enf.tenants["acme"] = true
	h.kv.writeErr = errors.New("no quorum")
	_, err := h.m.Clear(context.Background(), TenantBarrier("acme"), alice)
	var nc *RecordNotConfirmedError
	if !errors.As(err, &nc) {
		t.Fatalf("err = %v, want *RecordNotConfirmedError", err)
	}
	if !h.m.Refuses(TenantBarrier("acme")) {
		t.Fatal("gate opened although the clear was not recorded")
	}
}

// TestNodeSealWaitsForEveryAck: the node key is wiped only after every
// system-shard member acknowledged, and the gate is closed and the streams
// ended before the wait.
func TestNodeSealWaitsForEveryAck(t *testing.T) {
	h := newHarness(t)
	h.poll(t)
	sub := h.m.Streams().Subscribe("acme", "orders")
	h.kv.set(ackKey(peerB), []byte("1"))
	h.kv.set(ackKey(peerC), []byte("1"))
	var refusingAtWipe, streamEndedAtWipe bool
	h.enf.onSealNode = func() {
		refusingAtWipe = h.m.Refusing()
		streamEndedAtWipe = ended(sub)
	}
	start := h.clock.Now()
	if _, err := h.m.Seal(context.Background(), NodeBarrier(), ops); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !refusingAtWipe || !streamEndedAtWipe {
		t.Fatalf("at wipe: refusing=%v streamEnded=%v, want both", refusingAtWipe, streamEndedAtWipe)
	}
	if got := h.enf.callList(); !reflect.DeepEqual(got, []string{"node"}) {
		t.Fatalf("enforcement = %v", got)
	}
	if waited := h.clock.Now().Sub(start); waited >= h.m.cfg.AckTimeout {
		t.Fatalf("waited %v with every ack present", waited)
	}
	if raw, ok := h.kv.get(ackKey(self)); !ok || string(raw) != "1" {
		t.Fatalf("own ack = %q, %v", raw, ok)
	}
}

func TestNodeSealAckWaitIsBounded(t *testing.T) {
	h := newHarness(t)
	h.poll(t)
	h.kv.set(ackKey(peerB), []byte("1"))
	// peerC never acknowledges.
	start := h.clock.Now()
	if _, err := h.m.Seal(context.Background(), NodeBarrier(), ops); err != nil {
		t.Fatal(err)
	}
	waited := h.clock.Now().Sub(start)
	if waited < h.m.cfg.AckTimeout || waited > h.m.cfg.AckTimeout+2*h.m.cfg.PollInterval {
		t.Fatalf("waited %v, want about the ack timeout %v", waited, h.m.cfg.AckTimeout)
	}
	if got := h.enf.callList(); !reflect.DeepEqual(got, []string{"node"}) {
		t.Fatalf("enforcement = %v: the key must be wiped after the timeout", got)
	}
}

// TestNodeSealAckWaitReadsTheLocalCopy is the last node to see every
// acknowledgement: its peers have already paused and left the system shard,
// so a quorum read can no longer answer it. The wait must read its own copy,
// which already holds every acknowledgement, and wipe without waiting out the
// ack timeout.
func TestNodeSealAckWaitReadsTheLocalCopy(t *testing.T) {
	h := newHarness(t)
	h.poll(t)
	h.kv.set(ackKey(peerB), []byte("1"))
	h.kv.set(ackKey(peerC), []byte("1"))
	h.kv.onScan = func() { t.Error("the ack wait asked a quorum that the peers' pause removed") }
	start := h.clock.Now()
	if _, err := h.m.Seal(context.Background(), NodeBarrier(), ops); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if waited := h.clock.Now().Sub(start); waited >= h.m.cfg.AckTimeout {
		t.Fatalf("waited %v with every ack in the local copy", waited)
	}
	if h.kv.staleScans == 0 {
		t.Fatal("the ack wait never read the local copy")
	}
	if got := h.enf.callList(); !reflect.DeepEqual(got, []string{"node"}) {
		t.Fatalf("enforcement = %v", got)
	}
}

// TestNodeSealAckWaitBoundsTheAckWrite is a node whose own acknowledgement
// cannot commit: the write blocks as it does without a quorum. AckTimeout
// bounds the whole wait, the write included, so the key is still wiped.
func TestNodeSealAckWaitBoundsTheAckWrite(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.AckTimeout = 50 * time.Millisecond })
	h.poll(t)
	h.kv.set(ackKey(peerB), []byte("1"))
	h.kv.set(ackKey(peerC), []byte("1"))
	h.kv.mu.Lock()
	h.kv.blockPut = true
	h.kv.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := h.m.Seal(context.Background(), NodeBarrier(), ops)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the ack wait is not bounded: a blocked acknowledgement write held it")
	}
	if got := h.enf.callList(); !reflect.DeepEqual(got, []string{"node"}) {
		t.Fatalf("enforcement = %v: the key must be wiped after the timeout", got)
	}
}

func TestNodeSealAckFromOlderEpochDoesNotCount(t *testing.T) {
	h := newHarness(t)
	h.putRecord(t, Record{Barrier: NodeBarrier(), Epoch: 2, Sealed: false})
	h.poll(t)
	h.kv.set(ackKey(peerB), []byte("1"))
	h.kv.set(ackKey(peerC), []byte("garbage"))
	start := h.clock.Now()
	if _, err := h.m.Seal(context.Background(), NodeBarrier(), ops); err != nil {
		t.Fatal(err)
	}
	if h.clock.Now().Sub(start) < h.m.cfg.AckTimeout {
		t.Fatal("a stale or corrupt ack satisfied the wait for epoch 3")
	}
}

// TestRemoteNodeSealIsEnforcedByPoll: another node's node-barrier seal reaches
// this node: it acks, then wipes.
func TestRemoteNodeSealIsEnforcedByPoll(t *testing.T) {
	h := newHarness(t)
	h.poll(t)
	h.putRecord(t, Record{Barrier: NodeBarrier(), Epoch: 1, Sealed: true, Node: peerB})
	h.kv.set(ackKey(peerB), []byte("1"))
	h.kv.set(ackKey(peerC), []byte("1"))
	h.poll(t)
	if got := h.enf.callList(); !reflect.DeepEqual(got, []string{"node"}) {
		t.Fatalf("enforcement = %v", got)
	}
	if !h.m.Refusing() {
		t.Fatal("node gate open after a node seal")
	}
	if got := h.m.Status(NodeBarrier()).Local; got != LocalSealed {
		t.Fatalf("local = %q, want sealed", got)
	}
}

// TestRestartWithNodeSealedIsHeld: a node that restarted and unsealed at boot
// while the node record is sealed comes up held: it keeps its key, refuses
// requests, and is not sealed again for that epoch. A clear lets it serve; a
// newer seal seals it.
func TestRestartWithNodeSealedIsHeld(t *testing.T) {
	h := newHarness(t)
	h.putRecord(t, Record{Barrier: NodeBarrier(), Epoch: 3, Sealed: true, Node: peerB})
	h.poll(t)
	h.poll(t)
	if got := h.enf.callList(); len(got) != 0 {
		t.Fatalf("a held node sealed itself: %v", got)
	}
	if !h.m.Refusing() {
		t.Fatal("a held node served")
	}
	if got := h.m.Status(NodeBarrier()).Local; got != LocalHeld {
		t.Fatalf("local = %q, want held", got)
	}
	rec, err := h.m.Clear(context.Background(), NodeBarrier(), ops)
	if err != nil || rec.Epoch != 4 || rec.Sealed {
		t.Fatalf("Clear = %+v, %v", rec, err)
	}
	if h.m.Refusing() {
		t.Fatal("still refusing after cluster unseal")
	}
	h.putRecord(t, Record{Barrier: NodeBarrier(), Epoch: 5, Sealed: true, Node: peerC})
	h.poll(t)
	if got := h.enf.callList(); !reflect.DeepEqual(got, []string{"node"}) {
		t.Fatalf("a newer seal was not enforced: %v", got)
	}
}

func TestClusterUnsealRequiresTheLocalUnseal(t *testing.T) {
	h := newHarness(t)
	h.putRecord(t, Record{Barrier: NodeBarrier(), Epoch: 1, Sealed: true})
	h.poll(t)
	h.enf.node = false
	if _, err := h.m.Clear(context.Background(), NodeBarrier(), ops); !errors.Is(err, seal.ErrSealed) {
		t.Fatalf("err = %v, want seal.ErrSealed", err)
	}
	if rec, _ := h.record(t, NodeBarrier()); !rec.Sealed {
		t.Fatal("the record was cleared by a node that had not unsealed itself")
	}
}

// TestRestartWithTenantSealedSealsIt: a system-managed tenant auto-unseals at
// boot; a sealed record seals it on the first read.
func TestRestartWithTenantSealedSealsIt(t *testing.T) {
	h := newHarness(t)
	h.enf.tenants["acme"] = true
	h.putRecord(t, Record{Barrier: TenantBarrier("acme"), Epoch: 2, Sealed: true})
	h.poll(t)
	if got := h.enf.callList(); !reflect.DeepEqual(got, []string{"tenant/acme"}) {
		t.Fatalf("enforcement = %v", got)
	}
}

// TestCutOffStopsServingKeepsKeysAndResumes is the cut-off rule.
func TestCutOffStopsServingKeepsKeysAndResumes(t *testing.T) {
	h := newHarness(t)
	h.enf.tenants["acme"] = true
	h.poll(t)
	sub := h.m.Streams().Subscribe("acme", "orders")

	h.kv.readErr = errors.New("partitioned")
	h.clock.Advance(14 * time.Second)
	h.poll(t)
	if h.m.Refusing() {
		t.Fatal("refused inside the lease")
	}
	h.clock.Advance(2 * time.Second)
	h.poll(t)
	if !h.m.Refusing() {
		t.Fatal("served after the lease ran out")
	}
	if got := h.m.Status(TenantBarrier("acme")).Local; got != LocalCutOff {
		t.Fatalf("local = %q, want cut_off", got)
	}
	if !ended(sub) {
		t.Fatal("open stream survived the cut-off")
	}
	var unconfirmed *SealStateUnconfirmedError
	if !errors.As(sub.Cause(), &unconfirmed) {
		t.Fatalf("cause = %v, want *SealStateUnconfirmedError", sub.Cause())
	}
	if len(h.enf.callList()) != 0 || !h.enf.TenantUnsealed("acme") || !h.enf.NodeUnsealed() {
		t.Fatal("a cut-off node dropped a key")
	}

	h.kv.readErr = nil
	h.poll(t)
	if h.m.Refusing() {
		t.Fatal("did not resume after reading the records again")
	}
	if len(h.enf.callList()) != 0 {
		t.Fatal("resumed with a re-seal")
	}
}

func TestCutOffNodeSealsOnReconnectWhenSealedMeanwhile(t *testing.T) {
	h := newHarness(t)
	h.enf.tenants["acme"] = true
	h.poll(t)
	h.kv.readErr = errors.New("partitioned")
	h.clock.Advance(20 * time.Second)
	h.poll(t)
	h.putRecord(t, Record{Barrier: TenantBarrier("acme"), Epoch: 1, Sealed: true, Node: peerB})
	h.kv.readErr = nil
	h.poll(t)
	if got := h.enf.callList(); !reflect.DeepEqual(got, []string{"tenant/acme"}) {
		t.Fatalf("enforcement on reconnect = %v", got)
	}
}

// TestLeaseIsMeasuredFromBeforeTheRead: a slow read shortens the lease rather
// than extending it.
func TestLeaseIsMeasuredFromBeforeTheRead(t *testing.T) {
	h := newHarness(t)
	h.kv.onScan = func() { h.clock.Advance(10 * time.Second) }
	h.poll(t)
	h.kv.onScan = nil
	h.clock.Advance(5*time.Second + time.Millisecond)
	if !h.m.Refusing() {
		t.Fatal("the lease ran from after the read")
	}
}

func TestEnforcementFailureIsRetriedAndGateStaysClosed(t *testing.T) {
	h := newHarness(t)
	h.enf.tenants["acme"] = true
	h.poll(t)
	h.enf.setErr(errors.New("shard close failed"))
	h.putRecord(t, Record{Barrier: TenantBarrier("acme"), Epoch: 1, Sealed: true, Node: peerB})
	h.poll(t)
	if !h.m.Refuses(TenantBarrier("acme")) {
		t.Fatal("gate opened after a failed enforcement")
	}
	h.enf.setErr(nil)
	h.poll(t)
	if got := h.enf.callList(); len(got) != 2 {
		t.Fatalf("enforcement attempts = %v, want a retry", got)
	}
	h.poll(t)
	if got := h.enf.callList(); len(got) != 2 {
		t.Fatalf("enforced again after success: %v", got)
	}
}

func TestCorruptRecordClosesItsGate(t *testing.T) {
	h := newHarness(t)
	h.kv.set(recordKey(TenantBarrier("acme")), []byte("{"))
	h.putRecord(t, Record{Barrier: TenantBarrier("other"), Epoch: 1, Sealed: false})
	h.poll(t)
	if !h.m.Refuses(TenantBarrier("acme")) {
		t.Fatal("a record that cannot be read left its barrier served")
	}
	if h.m.Refuses(TenantBarrier("other")) || h.m.Refusing() {
		t.Fatal("a corrupt record closed other gates")
	}
}

func TestRunPollsUntilCancelled(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	h.kv.onScan = func() {
		h.kv.mu.Lock()
		n := h.kv.scans
		h.kv.mu.Unlock()
		if n >= 3 {
			cancel()
		}
	}
	done := make(chan struct{})
	go func() { h.m.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// TestActivationLaterStartsEnforcing: activation needs a fresh lease, which
// the inactive monitor's own reads provide. One whose reads stopped before
// activation holds no fresh lease when the feature turns on, so it refuses
// until it reads again.
func TestActivationLaterStartsEnforcing(t *testing.T) {
	h := newHarness(t)
	h.gate.set(false)
	h.poll(t)
	if h.m.Refusing() {
		t.Fatal("refused while inactive")
	}
	h.kv.mu.Lock()
	h.kv.readErr = errors.New("unreadable")
	h.kv.mu.Unlock()
	h.clock.Advance(time.Minute)
	h.poll(t)
	if h.m.Refusing() {
		t.Fatal("refused while inactive")
	}
	h.gate.set(true)
	if !h.m.Refusing() {
		t.Fatal("served without a fresh lease once active")
	}
	h.kv.mu.Lock()
	h.kv.readErr = nil
	h.kv.mu.Unlock()
	h.poll(t)
	if h.m.Refusing() {
		t.Fatal("refusing after the first read")
	}
}

func TestStatusReportsRecordAndLocal(t *testing.T) {
	h := newHarness(t)
	h.enf.tenants["acme"] = true
	h.poll(t)
	if st := h.m.Status(TenantBarrier("acme")); !st.Active || st.Record.Epoch != 0 || st.Local != LocalServing {
		t.Fatalf("status = %+v", st)
	}
	h.enf.tenants["acme"] = false
	if st := h.m.Status(TenantBarrier("acme")); st.Local != LocalSealed {
		t.Fatalf("local = %q for a locally sealed tenant", st.Local)
	}
	for i := 1; i <= 3; i++ {
		h.putRecord(t, Record{Barrier: TenantBarrier("t" + strconv.Itoa(i)), Epoch: uint64(i), Sealed: i%2 == 1})
	}
	h.poll(t)
	if st := h.m.Status(TenantBarrier("t3")); st.Record.Epoch != 3 || !st.Record.Sealed {
		t.Fatalf("status = %+v", st)
	}
}

// TestMonitor_RefusesUntilActivationsAreLoaded pins the restart rule on a
// composer whose feature gate starts after its servers: until the gate has
// read the cluster's activations, this node cannot know whether cluster
// sealing is active -- or whether its node record says sealed -- so it
// refuses rather than serve on a guess. Once the activations are read and
// cluster sealing is not active, it serves; with no gate at all (standalone)
// it never refuses on this account.
func TestMonitor_RefusesUntilActivationsAreLoaded(t *testing.T) {
	gate := &fakeGate{unloaded: true}
	m, err := NewMonitor(Config{KV: newMemKV(), Gate: gate, Enforcer: newFakeEnforcer(), Self: "1",
		Members: func(context.Context) ([]string, error) { return []string{"1"}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Refusing() {
		t.Fatal("serving before the feature gate read the activations")
	}
	gate.setLoaded(true)
	if m.Refusing() {
		t.Fatal("refusing with the activations read and cluster sealing inactive")
	}

	standalone, err := NewMonitor(Config{KV: newMemKV(), Enforcer: newFakeEnforcer(), Self: "1",
		Members: func(context.Context) ([]string, error) { return []string{"1"}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if standalone.Refusing() {
		t.Fatal("a monitor with no gate refuses")
	}
}

// TestRefusesEveryBarrierUntilTheFirstRead pins that a tenant or database
// barrier is refused -- at request resolution, and by the tenant shard
// reconciler, which asks the same question before starting a shard -- until
// this node knows its record: before the gate has read the activations, and,
// with cluster sealing active, before the monitor's first read. A sealed
// tenant's shards must not start in that window. After the read a clear
// record serves.
func TestRefusesEveryBarrierUntilTheFirstRead(t *testing.T) {
	h := newHarness(t)
	h.gate.setLoaded(false)
	if !h.m.Refuses(TenantBarrier("acme")) || !h.m.Refuses(DatabaseBarrier("acme", "app")) {
		t.Fatal("a barrier was served before the gate read the activations")
	}
	h.gate.setLoaded(true)
	if !h.m.Refuses(TenantBarrier("acme")) || !h.m.Refuses(DatabaseBarrier("acme", "app")) {
		t.Fatal("a barrier was served before the monitor's first read")
	}
	h.poll(t)
	if h.m.Refuses(TenantBarrier("acme")) || h.m.Refuses(DatabaseBarrier("acme", "app")) {
		t.Fatal("a barrier with no sealed record was refused after the first read")
	}
}

// TestMissedSealIsEnforced pins that every node seals on a seal even when it
// never saw the sealed record: a node that misses a seal and the clear that
// followed (cut off, or between polls) finds a clear record two or more
// epochs past the last one it applied, and wipes its key then -- the owner
// unseals it like every other node. The first read after a boot is exempt:
// its keys come from this boot's own unseal.
func TestMissedSealIsEnforced(t *testing.T) {
	acme := TenantBarrier("acme")
	h := newHarness(t)
	h.enf.tenants["acme"] = true
	h.putRecord(t, Record{Barrier: acme, Epoch: 4, Sealed: false, Principal: "owner"})
	h.poll(t) // the first read after boot: applied, not enforced
	if got := h.enf.callList(); len(got) != 0 {
		t.Fatalf("the first read after boot enforced %v", got)
	}

	// Seen in order -- sealed at 5, cleared at 6 -- the clear enforces nothing
	// more than the seal did.
	h.putRecord(t, Record{Barrier: acme, Epoch: 5, Sealed: true, Principal: "owner"})
	h.poll(t)
	h.enf.tenants["acme"] = true // the owner unsealed it here
	h.putRecord(t, Record{Barrier: acme, Epoch: 6, Sealed: false, Principal: "owner"})
	h.poll(t)
	if got := h.enf.callList(); len(got) != 1 || got[0] != "tenant/acme" {
		t.Fatalf("calls = %v, want the one seal at epoch 5", got)
	}

	// Missed: sealed at 7 and cleared at 8 between two reads.
	h.putRecord(t, Record{Barrier: acme, Epoch: 8, Sealed: false, Principal: "owner"})
	h.poll(t)
	if got := h.enf.callList(); len(got) != 2 || got[1] != "tenant/acme" {
		t.Fatalf("calls = %v, want the missed seal at epoch 7 enforced", got)
	}
	if h.enf.TenantUnsealed("acme") {
		t.Fatal("the key survived a seal this node missed")
	}
}

// TestMissedSealEndsAStreamThatSubscribedDuringTheWipe: a missed seal is
// enforced on a CLEAR record, so no record closes the gate before streams
// end -- the wipe does, by sealing the barrier. A stream that subscribed
// between the first End* and the wipe passed its open check; the End* after
// the wipe ends it.
func TestMissedSealEndsAStreamThatSubscribedDuringTheWipe(t *testing.T) {
	acme := TenantBarrier("acme")
	h := newHarness(t)
	h.enf.tenants["acme"] = true
	h.putRecord(t, Record{Barrier: acme, Epoch: 4, Sealed: false, Principal: "owner"})
	h.poll(t) // first read after boot: applied, not enforced

	var late *Subscription
	h.enf.onSealTenant = func() { late = h.m.Streams().Subscribe("acme", "ledger") }
	// Missed: sealed at 5 and cleared at 6 between two reads.
	h.putRecord(t, Record{Barrier: acme, Epoch: 6, Sealed: false, Principal: "owner"})
	h.poll(t)
	if late == nil {
		t.Fatal("precondition: the missed seal was not enforced")
	}
	if !ended(late) {
		t.Fatal("a stream that subscribed during the wipe outlived the missed seal")
	}
}

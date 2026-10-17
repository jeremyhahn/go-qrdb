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

package store_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/store"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// These tests drive the two audit-serving raft state machines -- Pebble and
// MVCC -- through the same propose path a real node uses
// (service.Host.SyncPropose carrying protocol.OpAuditAppend), and restart or
// snapshot them exactly as durable_lease_test.go does for leases. They are
// this feature's "restart and snapshot install keep the chain" proof at the
// state-machine level, complementing pkg/engine's determinism tests and
// pkg/audit's in-isolation Chain tests.

// auditNode is one incarnation of a replica serving the replicated audit
// chain.
type auditNode struct {
	sm    sm.DiskStateMachine
	chain *audit.Chain
	host  service.Host
}

type auditSMKind struct {
	name string
	new  func(l *vlog.Chain) (sm.DiskStateMachine, func() storage.Reader)
}

var auditSMKinds = []auditSMKind{
	{name: "pebble", new: func(l *vlog.Chain) (sm.DiskStateMachine, func() storage.Reader) {
		s := store.NewPebbleStateMachine(nil)
		s.SetLogs(l)
		return s, func() storage.Reader { return pebbleStorage.NewFromDB(s.DB(), true) }
	}},
	{name: "mvcc", new: func(l *vlog.Chain) (sm.DiskStateMachine, func() storage.Reader) {
		s := store.NewMVCCStateMachine(nil)
		s.SetLogs(l)
		return s, func() storage.Reader { return s.Store().Storage() }
	}},
}

// openAuditNode starts an incarnation of the replica whose state lives in
// dir, with a fresh Chain.
func openAuditNode(t *testing.T, kind auditSMKind, dir string) (*auditNode, func() storage.Reader) {
	t.Helper()
	logs := vlog.NewChain()
	chain := audit.ChainOn(logs)
	s, readerFn := kind.new(logs)
	if _, err := s.Open(context.Background(), dir, make(chan struct{})); err != nil {
		t.Fatalf("%s: open: %v", kind.name, err)
	}
	host := backend.NewLocalBackend(s)
	return &auditNode{sm: s, chain: chain, host: host}, readerFn
}

// append proposes one audit event for tenantID and returns the assigned
// sequence.
func (n *auditNode) append(t *testing.T, tenantID string, ev audit.AuditEvent) uint64 {
	t.Helper()
	cmd, err := protocol.EncodeAuditAppend(tenantID, ev)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	result, err := n.host.SyncPropose(context.Background(), 1, cmd)
	if err != nil {
		t.Fatalf("propose append: %v", err)
	}
	return result.Value
}

// stop ends the incarnation, as a process exit does.
func (n *auditNode) stop(t *testing.T) {
	t.Helper()
	if err := n.sm.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func fixedAuditEvent(tenantID string) audit.AuditEvent {
	return audit.AuditEvent{
		TenantID:  tenantID,
		Actor:     "operator",
		Resource:  "barrier",
		Action:    "unseal",
		Outcome:   audit.OutcomeSuccess,
		Timestamp: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
	}
}

// TestDurableAudit_SurvivesRestart asserts a restart -- a new state machine
// and a new Chain over the same directory, exactly as a process restart
// does -- continues the same tenant's chain rather than restarting it.
func TestDurableAudit_SurvivesRestart(t *testing.T) {
	for _, kind := range auditSMKinds {
		t.Run(kind.name, func(t *testing.T) {
			dir := t.TempDir()

			n, _ := openAuditNode(t, kind, dir)
			seq1 := n.append(t, "acme", fixedAuditEvent("acme"))
			if seq1 != 1 {
				t.Fatalf("first sequence = %d, want 1", seq1)
			}
			n.stop(t)

			n, reader := openAuditNode(t, kind, dir)
			defer n.stop(t)
			seq2 := n.append(t, "acme", fixedAuditEvent("acme"))
			if seq2 != 2 {
				t.Fatalf("sequence after restart = %d, want 2 (continuing the chain)", seq2)
			}

			page, err := n.chain.Query(reader(), "acme", audit.Filter{Limit: 100})
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			if len(page.Events) != 2 {
				t.Fatalf("got %d events after restart, want 2", len(page.Events))
			}
		})
	}
}

// TestDurableAudit_TravelsInASnapshot asserts the replicated audit chain is
// part of the ordinary snapshot a new replica catches up through: a
// follower with a chain of its own for a DIFFERENT tenant recovers the
// leader's snapshot and ends up with the leader's chain for "acme" --
// proof the audit table rides the same snapshot path as every other
// replicated record, per this feature's reserved-keyspace design.
func TestDurableAudit_TravelsInASnapshot(t *testing.T) {
	for _, kind := range auditSMKinds {
		t.Run(kind.name, func(t *testing.T) {
			ctx := context.Background()
			leader, leaderReader := openAuditNode(t, kind, t.TempDir())
			defer leader.stop(t)
			leader.append(t, "acme", fixedAuditEvent("acme"))
			leader.append(t, "acme", fixedAuditEvent("acme"))

			var buf bytes.Buffer
			snap, err := leader.sm.PrepareSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			if err := leader.sm.SaveSnapshot(ctx, snap, &buf, make(chan struct{})); err != nil {
				t.Fatal(err)
			}

			// The follower holds a chain of its own, for a tenant the
			// snapshot never names, so a recovery that merged rather than
			// replaced ranges would leave this alongside the leader's.
			follower, followerReader := openAuditNode(t, kind, t.TempDir())
			defer follower.stop(t)
			follower.append(t, "widgets", fixedAuditEvent("widgets"))

			if err := follower.sm.RecoverFromSnapshot(ctx, &buf, make(chan struct{})); err != nil {
				t.Fatalf("recover: %v", err)
			}

			leaderPage, err := leader.chain.Query(leaderReader(), "acme", audit.Filter{Limit: 100})
			if err != nil {
				t.Fatalf("query leader: %v", err)
			}

			// The follower's OWN Chain instance has stale in-memory state
			// from before the snapshot; the state machine always re-derives
			// (PebbleStateMachine.restoreAuditChain, MVCCEngine's own
			// restore-on-construction) the chain it exposes via AuditChain()
			// after a recovery, which is the one a real query path reads.
			recoveredChain := follower.sm.(interface {
				AuditChain() *audit.Chain
			}).AuditChain()

			page, err := recoveredChain.Query(followerReader(), "acme", audit.Filter{Limit: 100})
			if err != nil {
				t.Fatalf("query follower (acme): %v", err)
			}
			if len(page.Events) != len(leaderPage.Events) {
				t.Fatalf("follower acme events = %d, want %d (the leader's snapshot)", len(page.Events), len(leaderPage.Events))
			}
			for i := range page.Events {
				if page.Events[i].PrevHash != leaderPage.Events[i].PrevHash {
					t.Fatalf("event %d: PrevHash diverged after snapshot recovery: got %q, want %q",
						i, page.Events[i].PrevHash, leaderPage.Events[i].PrevHash)
				}
			}

			widgetsPage, err := recoveredChain.Query(followerReader(), "widgets", audit.Filter{Limit: 100})
			if err != nil {
				t.Fatalf("query follower (widgets): %v", err)
			}
			if len(widgetsPage.Events) != 0 {
				t.Fatalf("the follower's pre-snapshot tenant survived a recovery that should have replaced all data: got %d events", len(widgetsPage.Events))
			}
		})
	}
}

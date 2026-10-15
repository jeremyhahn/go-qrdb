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

package main

import (
	"context"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// chainGroup is one Raft group's state machine as a node runs it: built by
// the node's create function, opened on its own directory, and proposed to
// through the same Host path a real node uses.
type chainGroup struct {
	sm   sm.DiskStateMachine
	host service.Host
}

// openChainGroup builds a state machine from createFn for shardID and opens
// it on dir. Opening restores the audit chain's heads from dir's storage,
// which is also what a restart or a recovery does.
func openChainGroup(t *testing.T, createFn sm.CreateFunc, shardID uint64, dir string) *chainGroup {
	t.Helper()
	machine, ok := createFn(shardID, 1).(sm.DiskStateMachine)
	if !ok {
		t.Fatalf("shard %d: the create function built no sm.DiskStateMachine", shardID)
	}
	if _, err := machine.Open(context.Background(), dir, make(chan struct{})); err != nil {
		t.Fatalf("shard %d: open: %v", shardID, err)
	}
	return &chainGroup{sm: machine, host: backend.NewLocalBackend(machine)}
}

// appendEvent applies one audit append to the group and returns the
// sequence number its chain assigned.
func (g *chainGroup) appendEvent(t *testing.T, tenantID string) uint64 {
	t.Helper()
	cmd, err := protocol.EncodeAuditAppend(tenantID, audit.AuditEvent{
		TenantID:  tenantID,
		Actor:     "operator",
		Resource:  "barrier",
		Action:    "unseal",
		Outcome:   audit.OutcomeSuccess,
		Timestamp: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	result, err := g.host.SyncPropose(context.Background(), 1, cmd)
	if err != nil {
		t.Fatalf("propose append: %v", err)
	}
	return result.Value
}

func (g *chainGroup) close(t *testing.T) {
	t.Helper()
	if err := g.sm.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestBuildSMCreateFunc_EveryStateMachineHasItsOwnAuditChain starts two Raft
// groups on one node from ONE create function and proves their audit chains
// share nothing. audit.Chain.Restore replaces the whole head table with what
// one state machine's storage holds, so a chain shared across groups would
// number one group's events after the other's, and reopening either group
// would overwrite the other's heads.
func TestBuildSMCreateFunc_EveryStateMachineHasItsOwnAuditChain(t *testing.T) {
	const tenant = "acme"
	for _, smType := range []string{"mvcc", "pebble"} {
		t.Run(smType, func(t *testing.T) {
			hub := watch.NewHub()
			t.Cleanup(hub.Close)
			f := &startFlags{smType: smType, dataDir: t.TempDir()}
			createFn, err := buildSMCreateFunc(f, atrest.New(nil), lease.NewLessor(), hub)
			if err != nil {
				t.Fatalf("buildSMCreateFunc(%s): %v", smType, err)
			}
			dirA, dirB := t.TempDir(), t.TempDir()

			a := openChainGroup(t, createFn, 1, dirA)
			b := openChainGroup(t, createFn, 2, dirB)
			if a.sm.(auditChainHolder).AuditChain() == b.sm.(auditChainHolder).AuditChain() {
				t.Error("two state machines from one create function share one audit.Chain")
			}

			if seq := a.appendEvent(t, tenant); seq != 1 {
				t.Fatalf("group A first append: seq %d, want 1", seq)
			}
			if seq := b.appendEvent(t, tenant); seq != 1 {
				t.Fatalf("group B first append: seq %d, want 1 (numbered after group A's event)", seq)
			}
			if seq := a.appendEvent(t, tenant); seq != 2 {
				t.Fatalf("group A second append: seq %d, want 2", seq)
			}

			// Recovery: group B restarts from its own storage while group A
			// keeps running. B's restore must not touch A's heads.
			b.close(t)
			b = openChainGroup(t, createFn, 2, dirB)
			if seq := a.appendEvent(t, tenant); seq != 3 {
				t.Fatalf("group A after group B's restore: seq %d, want 3", seq)
			}
			if seq := b.appendEvent(t, tenant); seq != 2 {
				t.Fatalf("group B after its restore: seq %d, want 2", seq)
			}

			// And the other way round, with A reopened last.
			a.close(t)
			a = openChainGroup(t, createFn, 1, dirA)
			defer a.close(t)
			defer b.close(t)
			if seq := b.appendEvent(t, tenant); seq != 3 {
				t.Fatalf("group B after group A's restore: seq %d, want 3", seq)
			}
			if seq := a.appendEvent(t, tenant); seq != 4 {
				t.Fatalf("group A after its restore: seq %d, want 4", seq)
			}
		})
	}
}

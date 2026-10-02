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

package store

import (
	"bytes"
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mvccLeaseSMKind is the MVCC state machine: the one that applies the
// compaction-state command.
func mvccLeaseSMKind(t *testing.T) leaseSMKind {
	t.Helper()
	for _, k := range leaseSMKinds {
		if k.name == "mvcc" {
			return k
		}
	}
	t.Fatal("no mvcc state machine kind")
	return leaseSMKind{}
}

// setPolicy applies a compaction-state policy command.
func (h *leaseHarness) setPolicy(scope, policyJSON string) {
	h.t.Helper()
	h.applyOK(protocol.EncodeCompactionState(protocol.CompactionStateCommand{
		Kind: protocol.CompactionStateSetPolicy, Scope: scope, Policy: []byte(policyJSON),
	}))
}

// sample applies a compaction-state sample command.
func (h *leaseHarness) sample(scope string, at, revision int64) {
	h.t.Helper()
	h.applyOK(protocol.EncodeCompactionState(protocol.CompactionStateCommand{
		Kind: protocol.CompactionStateSample, Scope: scope, At: at, Revision: revision,
	}))
}

// compactionState reads scope's record through the state machine.
func (h *leaseHarness) compactionState(scope string) *compaction.State {
	h.t.Helper()
	got, err := h.sm.Lookup(context.Background(), service.CompactionStateQuery{Scope: scope})
	if err != nil {
		h.t.Fatalf("Lookup(CompactionStateQuery): %v", err)
	}
	state, _ := got.(*compaction.State)
	return state
}

// TestCompactionState_HashCoversTheTable: the compaction table is replicated
// state, so the replica hash covers it -- a replica holding a policy the
// other lacks must not hash the same.
func TestCompactionState_HashCoversTheTable(t *testing.T) {
	kind := mvccLeaseSMKind(t)
	a, b := newLeaseHarness(t, kind), newLeaseHarness(t, kind)
	b.index = 1000 // different log positions: last-applied must not matter

	a.put("k", "v", 0)
	b.put("k", "v", 0)
	if hashOf(t, a) != hashOf(t, b) {
		t.Fatal("identical state hashes differently")
	}
	a.setPolicy("acme/app/", `{"policy":{"mode":"period","retainPeriod":"1h"}}`)
	if hashOf(t, a) == hashOf(t, b) {
		t.Fatal("a replica holding a compaction policy the other lacks hashes the same: the hash does not cover the compaction table")
	}
	b.setPolicy("acme/app/", `{"policy":{"mode":"period","retainPeriod":"1h"}}`)
	if hashOf(t, a) != hashOf(t, b) {
		t.Fatal("replicas that applied the same policy hash differently")
	}
	a.sample("acme/app/", 1_000, 1)
	if hashOf(t, a) == hashOf(t, b) {
		t.Fatal("a replica holding a sample the other lacks hashes the same")
	}
}

// TestCompactionState_SurvivesSnapshotRecovery: a snapshot -- what a lagging
// replica recovers from and what a backup is made of -- carries the table,
// and recovery replaces the replica's own records rather than merging.
func TestCompactionState_SurvivesSnapshotRecovery(t *testing.T) {
	kind := mvccLeaseSMKind(t)
	src := newLeaseHarness(t, kind)
	src.put("acme/app/k", "v", 0)
	src.setPolicy("acme/app/", `{"policy":{"mode":"period","retainPeriod":"1h"}}`)
	src.sample("acme/app/", 1_000, 1)
	src.setPolicy(compaction.NodeScope, `{"policy":{"mode":"revision","retainRevisions":10}}`)

	snapCtx, err := src.sm.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	var buf bytes.Buffer
	if err := src.sm.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	dst := newLeaseHarness(t, kind)
	dst.setPolicy("beta/app/", `{"policy":{"mode":"revision","retainRevisions":5}}`) // the replica's own, to be replaced
	if err := dst.sm.RecoverFromSnapshot(context.Background(), &buf, nil); err != nil {
		t.Fatalf("RecoverFromSnapshot: %v", err)
	}

	got := dst.compactionState("acme/app/")
	if got == nil || got.Policy == nil || got.Policy.RetainPeriod != "1h" || len(got.Samples) != 1 || got.Samples[0].Revision != 1 {
		t.Fatalf("acme/app/ after recovery = %+v, want the source's policy and sample", got)
	}
	if node := dst.compactionState(compaction.NodeScope); node == nil || node.Policy.RetainRevisions != 10 {
		t.Fatalf("node scope after recovery = %+v", node)
	}
	if stale := dst.compactionState("beta/app/"); stale != nil {
		t.Fatalf("the replica's own record survived recovery: %+v", stale)
	}
	if hashOf(t, src) != hashOf(t, dst) {
		t.Fatal("recovered replica hashes differently from its source")
	}
}

// TestCompactionState_SurvivesRestart: the record is in the state machine's
// own durable store, so a restarted node reads it back -- standalone, this is
// the whole of what keeps a schedule across restarts.
func TestCompactionState_SurvivesRestart(t *testing.T) {
	h := newLeaseHarness(t, mvccLeaseSMKind(t))
	h.setPolicy("acme/app/", `{"policy":{"mode":"period","retainPeriod":"1h"}}`)
	h.sample("acme/app/", 1_000, 1)
	h.restart()
	got := h.compactionState("acme/app/")
	if got == nil || got.Policy == nil || len(got.Samples) != 1 {
		t.Fatalf("state after restart = %+v", got)
	}
}

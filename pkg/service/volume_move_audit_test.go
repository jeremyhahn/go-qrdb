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

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

func TestVolumeMoveAudit_RecordsTheStart(t *testing.T) {
	ring := audit.NewRingBuffer(16)
	a := NewVolumeMoveAudit(ring, audit.FailClosed)
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "root"})
	req := volume.MoveTenantRequest{TenantID: "acme", ToPool: "bulk"}
	settle, err := a.BeginMoveStart(ctx, req, volume.MoveStartFacts{})
	if err != nil {
		t.Fatalf("BeginMoveStart: %v", err)
	}
	if n := len(ring.Snapshot()); n != 1 {
		t.Fatalf("recorded %d events before settle, want the permitted record", n)
	}
	settle(&volume.Move{TenantID: "acme", FromPool: "fast", ToPool: "bulk"}, nil)
	evs := ring.Snapshot()
	if len(evs) != 2 {
		t.Fatalf("recorded %d events, want the permitted record and its outcome", len(evs))
	}
	for i, want := range []string{audit.OutcomePermitted, audit.OutcomeSuccess} {
		ev := evs[i]
		if ev.Action != ActionVolumePoolMoveStart || ev.Outcome != want {
			t.Fatalf("event %d = %s/%s, want %s/%s", i, ev.Action, ev.Outcome, ActionVolumePoolMoveStart, want)
		}
		if ev.TenantID != "acme" || ev.Resource != "volume_pool" || ev.Actor != "root" {
			t.Fatalf("event %d = %+v, want TenantID=acme Resource=volume_pool Actor=root", i, ev)
		}
		if ev.Attributes["to_pool"] != "bulk" {
			t.Fatalf("event %d attributes = %v, want to_pool=bulk", i, ev.Attributes)
		}
		// to_pool, ruling 1234's allow_unencrypted_destination and
		// encryption_dropped, and on the outcome from_pool.
		if ev.Attributes["allow_unencrypted_destination"] != false || ev.Attributes["encryption_dropped"] != false {
			t.Fatalf("event %d attributes = %v, want allow_unencrypted_destination=false encryption_dropped=false", i, ev.Attributes)
		}
		if wantKeys := i + 3; len(ev.Attributes) != wantKeys {
			t.Fatalf("event %d attributes = %v, want %d keys", i, ev.Attributes, wantKeys)
		}
	}
	if _, ok := evs[0].Attributes["from_pool"]; ok {
		t.Fatalf("permitted record carries from_pool: %v", evs[0].Attributes)
	}
	if evs[1].Attributes["from_pool"] != "fast" {
		t.Fatalf("outcome attributes = %v, want from_pool=fast", evs[1].Attributes)
	}
}

func TestVolumeMoveAudit_FailureOutcome(t *testing.T) {
	ring := audit.NewRingBuffer(16)
	a := NewVolumeMoveAudit(ring, audit.FailClosed)
	settle, err := a.BeginMoveStart(context.Background(), volume.MoveTenantRequest{TenantID: "acme", ToPool: "bulk"}, volume.MoveStartFacts{})
	if err != nil {
		t.Fatalf("BeginMoveStart: %v", err)
	}
	settle(nil, &volume.MoveConflictError{TenantID: "acme", Reason: "a move is already running"})
	evs := ring.Snapshot()
	if len(evs) != 2 || evs[1].Outcome != audit.OutcomeFailure+":move-start-failed" {
		t.Fatalf("events = %+v, want a failure outcome", evs)
	}
	if _, ok := evs[1].Attributes["from_pool"]; ok {
		t.Fatalf("failure outcome carries from_pool with no move: %v", evs[1].Attributes)
	}
}

func TestVolumeMoveAudit_GateFollowsThePolicy(t *testing.T) {
	ctx := context.Background()
	req := volume.MoveTenantRequest{TenantID: "acme", ToPool: "bulk"}
	sink := &refusingAuditLogger{err: errors.New("audit sink unavailable")}
	var gate *audit.GateError
	if _, err := NewVolumeMoveAudit(sink, audit.FailClosed).BeginMoveStart(ctx, req, volume.MoveStartFacts{}); !errors.As(err, &gate) {
		t.Fatalf("fail-closed BeginMoveStart = %v, want *audit.GateError", err)
	}
	settle, err := NewVolumeMoveAudit(sink, audit.FailOpen).BeginMoveStart(ctx, req, volume.MoveStartFacts{})
	if err != nil {
		t.Fatalf("fail-open BeginMoveStart: %v", err)
	}
	settle(&volume.Move{FromPool: "fast"}, nil)
}

// TestVolumeMoveAudit_EncryptionDropRecord: ruling 1234, a start that drops
// the tenant's at-rest encryption first writes a
// volume_pool.move_encryption_drop record, then the move_start record,
// both carrying the operator's flag.
func TestVolumeMoveAudit_EncryptionDropRecord(t *testing.T) {
	ring := audit.NewRingBuffer(16)
	a := NewVolumeMoveAudit(ring, audit.FailClosed)
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "root"})
	req := volume.MoveTenantRequest{TenantID: "acme", ToPool: "bulk", AllowUnencryptedDestination: true}
	settle, err := a.BeginMoveStart(ctx, req, volume.MoveStartFacts{FromPool: "fast", EncryptionDropped: true})
	if err != nil {
		t.Fatalf("BeginMoveStart: %v", err)
	}
	settle(&volume.Move{TenantID: "acme", FromPool: "fast", ToPool: "bulk", EncryptionDropped: true}, nil)
	evs := ring.Snapshot()
	if len(evs) != 3 {
		t.Fatalf("recorded %d events, want the drop, the permitted start and its outcome", len(evs))
	}
	drop := evs[0]
	if drop.Action != ActionVolumePoolMoveEncryptionDrop || drop.Outcome != audit.OutcomePermitted {
		t.Fatalf("first event = %s/%s, want %s/%s", drop.Action, drop.Outcome, ActionVolumePoolMoveEncryptionDrop, audit.OutcomePermitted)
	}
	if drop.Actor != "root" || drop.TenantID != "acme" || drop.Resource != "volume_pool" {
		t.Fatalf("drop record = %+v, want Actor=root TenantID=acme Resource=volume_pool", drop)
	}
	wantDrop := map[string]any{"from_pool": "fast", "to_pool": "bulk", "encryption": "dropped", "allow_unencrypted_destination": true}
	if len(drop.Attributes) != len(wantDrop) {
		t.Fatalf("drop attributes = %v, want %v", drop.Attributes, wantDrop)
	}
	for k, v := range wantDrop {
		if drop.Attributes[k] != v {
			t.Fatalf("drop attributes = %v, want %v", drop.Attributes, wantDrop)
		}
	}
	for i, want := range []string{audit.OutcomePermitted, audit.OutcomeSuccess} {
		ev := evs[i+1]
		if ev.Action != ActionVolumePoolMoveStart || ev.Outcome != want {
			t.Fatalf("event %d = %s/%s, want %s/%s", i+1, ev.Action, ev.Outcome, ActionVolumePoolMoveStart, want)
		}
		if ev.Attributes["allow_unencrypted_destination"] != true || ev.Attributes["encryption_dropped"] != true {
			t.Fatalf("event %d attributes = %v, want allow_unencrypted_destination=true encryption_dropped=true", i+1, ev.Attributes)
		}
	}
}

// TestVolumeMoveAudit_EncryptionDropFailsClosedUnderFailOpen: the drop
// record is fail-closed whatever the configured policy; a start that drops
// nothing still follows the policy.
func TestVolumeMoveAudit_EncryptionDropFailsClosedUnderFailOpen(t *testing.T) {
	ctx := context.Background()
	sink := &refusingAuditLogger{err: errors.New("audit sink unavailable")}
	a := NewVolumeMoveAudit(sink, audit.FailOpen)
	req := volume.MoveTenantRequest{TenantID: "acme", ToPool: "bulk", AllowUnencryptedDestination: true}
	var gate *audit.GateError
	if _, err := a.BeginMoveStart(ctx, req, volume.MoveStartFacts{FromPool: "fast", EncryptionDropped: true}); !errors.As(err, &gate) {
		t.Fatalf("fail-open drop BeginMoveStart = %v, want *audit.GateError", err)
	}
	settle, err := a.BeginMoveStart(ctx, req, volume.MoveStartFacts{FromPool: "fast"})
	if err != nil {
		t.Fatalf("fail-open non-drop BeginMoveStart: %v", err)
	}
	settle(&volume.Move{FromPool: "fast"}, nil)
}

func TestVolumeMoveAudit_NilLogger(t *testing.T) {
	if a := NewVolumeMoveAudit(nil, audit.FailClosed); a != nil {
		t.Fatalf("NewVolumeMoveAudit(nil) = %v, want nil", a)
	}
}

func TestVolumeMoveTransitionAudit_RecordsEachTransition(t *testing.T) {
	if r := NewVolumeMoveTransitionAudit(nil); r != nil {
		t.Fatalf("NewVolumeMoveTransitionAudit(nil) = %v, want nil", r)
	}
	ring := audit.NewRingBuffer(16)
	r := NewVolumeMoveTransitionAudit(ring)
	ctx := context.Background()
	steps := []struct{ transition, node, phase, action, outcome string }{
		{volume.MoveTransitionComplete, "n1", volume.MoveStepPhaseCopy, "volume_pool.move_complete", audit.OutcomeSuccess},
		{volume.MoveTransitionFail, "n2", volume.MoveStepPhaseCopy, "volume_pool.move_fail", audit.OutcomeFailure + ":move-step-failed"},
		{volume.MoveTransitionCutover, "", "", "volume_pool.move_cutover", audit.OutcomeSuccess},
		{volume.MoveTransitionCancel, "", "", "volume_pool.move_cancel", audit.OutcomeSuccess},
	}
	for _, s := range steps {
		if err := r.RecordMoveTransition(ctx, s.transition, "acme", s.node, s.phase); err != nil {
			t.Fatalf("RecordMoveTransition(%s): %v", s.transition, err)
		}
	}
	evs := ring.Snapshot()
	if len(evs) != len(steps) {
		t.Fatalf("recorded %d events, want %d", len(evs), len(steps))
	}
	for i, s := range steps {
		ev := evs[i]
		if ev.Action != s.action || ev.Outcome != s.outcome || ev.TenantID != "acme" || ev.Resource != "volume_pool" {
			t.Fatalf("event %d = %+v, want %s/%s", i, ev, s.action, s.outcome)
		}
		if s.node == "" && len(ev.Attributes) != 0 {
			t.Fatalf("event %d attributes = %v, want none", i, ev.Attributes)
		}
		if s.node != "" && (ev.Attributes["node_id"] != s.node || ev.Attributes["phase"] != s.phase) {
			t.Fatalf("event %d attributes = %v", i, ev.Attributes)
		}
	}
	// A sink that refuses the write is never an error: the transition is
	// already in the journal.
	sink := &refusingAuditLogger{err: errors.New("audit sink unavailable")}
	if err := NewVolumeMoveTransitionAudit(sink).RecordMoveTransition(ctx, volume.MoveTransitionCutover, "acme", "", ""); err != nil {
		t.Fatalf("refused write = %v, want nil", err)
	}
}

// An operator's cancel, applied or refused, is recorded as
// volume_pool.move_cancel with the ctx's subject as the actor; a ctx with
// no identity (the reconciler's own transitions) records "system".
func TestVolumeMoveTransitionAudit_CancelCarriesTheOperator(t *testing.T) {
	ring := audit.NewRingBuffer(16)
	r := NewVolumeMoveTransitionAudit(ring)
	operator := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "ops-alice"})
	steps := []struct {
		ctx                        context.Context
		transition, actor, outcome string
	}{
		{operator, volume.MoveTransitionCancel, "ops-alice", audit.OutcomeSuccess},
		{operator, volume.MoveTransitionCancelRefused, "ops-alice", audit.OutcomeFailure + ":move-past-commit"},
		{context.Background(), volume.MoveTransitionCutover, "system", audit.OutcomeSuccess},
	}
	for _, s := range steps {
		if err := r.RecordMoveTransition(s.ctx, s.transition, "acme", "", ""); err != nil {
			t.Fatalf("RecordMoveTransition(%s): %v", s.transition, err)
		}
	}
	evs := ring.Snapshot()
	if len(evs) != len(steps) {
		t.Fatalf("recorded %d events, want %d", len(evs), len(steps))
	}
	for i, s := range steps {
		ev := evs[i]
		if ev.Actor != s.actor || ev.Outcome != s.outcome || ev.TenantID != "acme" {
			t.Fatalf("event %d = %+v, want actor %s outcome %s", i, ev, s.actor, s.outcome)
		}
	}
	for i := 0; i < 2; i++ {
		if evs[i].Action != "volume_pool.move_cancel" {
			t.Fatalf("event %d action = %q, want volume_pool.move_cancel", i, evs[i].Action)
		}
	}
}

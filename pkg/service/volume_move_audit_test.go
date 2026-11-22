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
	settle, err := a.BeginMoveStart(ctx, req)
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
		// Only to_pool, and on the outcome from_pool: no consent flag
		// attribute (decision Q9 is refused outright, ruling 1115).
		if wantKeys := i + 1; len(ev.Attributes) != wantKeys {
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
	settle, err := a.BeginMoveStart(context.Background(), volume.MoveTenantRequest{TenantID: "acme", ToPool: "bulk"})
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
	if _, err := NewVolumeMoveAudit(sink, audit.FailClosed).BeginMoveStart(ctx, req); !errors.As(err, &gate) {
		t.Fatalf("fail-closed BeginMoveStart = %v, want *audit.GateError", err)
	}
	settle, err := NewVolumeMoveAudit(sink, audit.FailOpen).BeginMoveStart(ctx, req)
	if err != nil {
		t.Fatalf("fail-open BeginMoveStart: %v", err)
	}
	settle(&volume.Move{FromPool: "fast"}, nil)
}

func TestVolumeMoveAudit_NilLogger(t *testing.T) {
	if a := NewVolumeMoveAudit(nil, audit.FailClosed); a != nil {
		t.Fatalf("NewVolumeMoveAudit(nil) = %v, want nil", a)
	}
}

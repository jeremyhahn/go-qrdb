// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// newAdmittedMove returns a catalog with acme's move from fast to bulk
// admitted over replica nodes n1 and n2.
func newAdmittedMove(t *testing.T) (*Catalog, *fakeKV, *fakeTx) {
	t.Helper()
	cat, kv, tx := newMoveTestCatalog(t)
	if _, err := cat.AdmitMove(context.Background(), admitAcme()); err != nil {
		t.Fatalf("AdmitMove: %v", err)
	}
	return cat, kv, tx
}

func TestCatalog_GrantMoveStep_PicksTheFirstPendingNode(t *testing.T) {
	cat, kv, _ := newAdmittedMove(t)
	ctx := context.Background()
	m, err := cat.GrantMoveStep(ctx, "acme")
	if err != nil {
		t.Fatalf("GrantMoveStep: %v", err)
	}
	if m.State != MoveStateCopying || m.StepNode != "n1" || m.StepAttempts != 0 || m.UpdatedAt.IsZero() {
		t.Fatalf("move = %+v, want copying with step n1", m)
	}
	if stored := storedMove(t, kv, "acme"); stored == nil || stored.StepNode != "n1" || stored.State != MoveStateCopying {
		t.Fatalf("journal = %+v, want copying with step n1", stored)
	}
	before := mustGet(t, kv, MoveKey("acme"))
	again, err := cat.GrantMoveStep(ctx, "acme")
	if err != nil {
		t.Fatalf("second GrantMoveStep: %v", err)
	}
	if again.StepNode != "n1" || !again.UpdatedAt.Equal(m.UpdatedAt) {
		t.Fatalf("second grant = %+v, want n1 unchanged", again)
	}
	if after := mustGet(t, kv, MoveKey("acme")); !bytes.Equal(before, after) {
		t.Fatal("second grant wrote the journal")
	}
}

func TestCatalog_CompleteMoveStep(t *testing.T) {
	cat, _, _ := newAdmittedMove(t)
	ctx := context.Background()
	if _, err := cat.GrantMoveStep(ctx, "acme"); err != nil {
		t.Fatalf("GrantMoveStep: %v", err)
	}
	m, err := cat.CompleteMoveStep(ctx, "acme", "n1")
	if err != nil {
		t.Fatalf("CompleteMoveStep n1: %v", err)
	}
	if m.State != MoveStateCopying || m.StepNode != "" ||
		!reflect.DeepEqual(m.NodesCopied, []string{"n1"}) || !reflect.DeepEqual(m.NodesPending, []string{"n2"}) {
		t.Fatalf("move after n1 = %+v", m)
	}
	m, err = cat.GrantMoveStep(ctx, "acme")
	if err != nil || m.StepNode != "n2" {
		t.Fatalf("GrantMoveStep after n1 = %+v, %v, want step n2", m, err)
	}
	m, err = cat.CompleteMoveStep(ctx, "acme", "n2")
	if err != nil {
		t.Fatalf("CompleteMoveStep n2: %v", err)
	}
	if m.State != MoveStateCopied || len(m.NodesPending) != 0 || !reflect.DeepEqual(m.NodesCopied, []string{"n1", "n2"}) {
		t.Fatalf("move after n2 = %+v, want copied", m)
	}
	got, err := cat.GetMove(ctx, "acme")
	if err != nil || got.State != MoveStateCopied {
		t.Fatalf("GetMove = %+v, %v, want copied", got, err)
	}
}

func TestCatalog_SettlingCopyStepHoldsTheMove(t *testing.T) {
	cat, _, _ := newAdmittedMove(t)
	ctx := context.Background()
	if _, err := cat.GrantMoveStep(ctx, "acme"); err != nil {
		t.Fatalf("GrantMoveStep: %v", err)
	}
	m, err := cat.RecordMoveStep(ctx, "acme", "n1")
	if err != nil {
		t.Fatalf("RecordMoveStep n1: %v", err)
	}
	if m.StepNode != "n1" || !m.StepSettling || m.State != MoveStateCopying ||
		!reflect.DeepEqual(m.NodesCopied, []string{"n1"}) || !reflect.DeepEqual(m.NodesPending, []string{"n2"}) {
		t.Fatalf("move after complete n1 = %+v, want n1 settling, copied [n1]", m)
	}
	if m, err = cat.GrantMoveStep(ctx, "acme"); err != nil || m.StepNode != "n1" {
		t.Fatalf("GrantMoveStep while n1 settles = %+v, %v, want the step kept by n1", m, err)
	}
	var conflict *MoveConflictError
	if _, err := cat.FailMoveStep(ctx, "acme", "n1", "late", 3); !errors.As(err, &conflict) {
		t.Fatalf("FailMoveStep of a settling step error = %v, want *MoveConflictError", err)
	}
	if m, err = cat.RecordMoveStep(ctx, "acme", "n1"); err != nil || !m.StepSettling || len(m.NodesCopied) != 1 {
		t.Fatalf("repeated RecordMoveStep = %+v, %v, want an idempotent settling step", m, err)
	}
	if _, err := cat.ReleaseMoveStep(ctx, "acme", "n2"); !errors.As(err, &conflict) {
		t.Fatalf("ReleaseMoveStep by a non-holder error = %v, want *MoveConflictError", err)
	}
	if m, err = cat.ReleaseMoveStep(ctx, "acme", "n1"); err != nil || m.StepNode != "" || m.StepSettling || m.State != MoveStateCopying {
		t.Fatalf("ReleaseMoveStep n1 = %+v, %v, want the step released, copying", m, err)
	}
	if _, err := cat.ReleaseMoveStep(ctx, "acme", "n1"); !errors.As(err, &conflict) {
		t.Fatalf("second ReleaseMoveStep error = %v, want *MoveConflictError", err)
	}
	if m, err = cat.GrantMoveStep(ctx, "acme"); err != nil || m.StepNode != "n2" {
		t.Fatalf("GrantMoveStep after the ack = %+v, %v, want n2", m, err)
	}
	if m, err = cat.RecordMoveStep(ctx, "acme", "n2"); err != nil || m.State != MoveStateCopying || !m.StepSettling {
		t.Fatalf("RecordMoveStep n2 = %+v, %v, want copying with n2 settling", m, err)
	}
	if _, err := cat.CutOverMove(ctx, "acme"); !errors.As(err, &conflict) {
		t.Fatalf("CutOverMove while the last step settles error = %v, want *MoveConflictError", err)
	}
	if m, err = cat.ReleaseMoveStep(ctx, "acme", "n2"); err != nil || m.State != MoveStateCopied || m.NodesPending != nil {
		t.Fatalf("ReleaseMoveStep n2 = %+v, %v, want copied", m, err)
	}
}

func TestCatalog_SettlingCopyStepCancelAbortsAtRelease(t *testing.T) {
	cat, _, _ := newAdmittedMove(t)
	ctx := context.Background()
	if _, err := cat.GrantMoveStep(ctx, "acme"); err != nil {
		t.Fatalf("GrantMoveStep: %v", err)
	}
	if _, err := cat.RecordMoveStep(ctx, "acme", "n1"); err != nil {
		t.Fatalf("RecordMoveStep: %v", err)
	}
	m, err := cat.CancelMove(ctx, "acme")
	if err != nil || !m.CancelRequested || m.State != MoveStateCopying || m.StepNode != "n1" {
		t.Fatalf("CancelMove while n1 settles = %+v, %v, want cancel requested, n1 kept", m, err)
	}
	var conflict *MoveConflictError
	if _, err := cat.GrantAbortStep(ctx, "acme"); !errors.As(err, &conflict) {
		t.Fatalf("GrantAbortStep before the ack error = %v, want *MoveConflictError", err)
	}
	if m, err = cat.ReleaseMoveStep(ctx, "acme", "n1"); err != nil || m.State != MoveStateAborting || m.CancelRequested || m.StepNode != "" {
		t.Fatalf("ReleaseMoveStep = %+v, %v, want aborting with no step", m, err)
	}
	if m, err = cat.GrantAbortStep(ctx, "acme"); err != nil || m.StepNode != "n1" {
		t.Fatalf("GrantAbortStep = %+v, %v, want the copied n1 first", m, err)
	}
}

// TestCatalog_ReleaseMoveStep_WithoutSettlingRefused: only the holder of a
// recorded, settling step releases it; a step granted but not recorded, or
// no step at all, is refused and the journal is left as it was.
// TestCatalog_RecordAbortStep_KeepsJournalAndReservation: a copied node's
// recorded abort step keeps its step, the journal and the destination
// reservation until ReleaseMoveStep; the last release frees both.
func TestCatalog_MoveStep_NodeWithoutTheStepRefused(t *testing.T) {
	cat, kv, _ := newAdmittedMove(t)
	ctx := context.Background()
	var conflict *MoveConflictError
	if _, err := cat.CompleteMoveStep(ctx, "acme", "n1"); !errors.As(err, &conflict) {
		t.Fatalf("CompleteMoveStep before a grant error = %v, want *MoveConflictError", err)
	}
	if _, err := cat.GrantMoveStep(ctx, "acme"); err != nil {
		t.Fatalf("GrantMoveStep: %v", err)
	}
	before := mustGet(t, kv, MoveKey("acme"))
	if _, err := cat.CompleteMoveStep(ctx, "acme", "n2"); !errors.As(err, &conflict) || conflict.NodeID != "n2" {
		t.Fatalf("CompleteMoveStep n2 error = %v, want *MoveConflictError for n2", err)
	}
	if _, err := cat.FailMoveStep(ctx, "acme", "n2", "boom", 0); !errors.As(err, &conflict) {
		t.Fatalf("FailMoveStep n2 error = %v, want *MoveConflictError", err)
	}
	if after := mustGet(t, kv, MoveKey("acme")); !bytes.Equal(before, after) {
		t.Fatal("a refused transition wrote the journal")
	}
}

func TestCatalog_FailMoveStep_BoundedAttempts(t *testing.T) {
	cat, _, _ := newAdmittedMove(t)
	ctx := context.Background()
	if _, err := cat.GrantMoveStep(ctx, "acme"); err != nil {
		t.Fatalf("GrantMoveStep: %v", err)
	}
	for i := 1; i < DefaultMoveStepAttempts; i++ {
		m, err := cat.FailMoveStep(ctx, "acme", "n1", "copy failed", 0)
		if err != nil {
			t.Fatalf("FailMoveStep %d: %v", i, err)
		}
		if m.State != MoveStateCopying || m.StepNode != "n1" || m.StepAttempts != i || m.Error != "copy failed" {
			t.Fatalf("move after failure %d = %+v", i, m)
		}
	}
	m, err := cat.FailMoveStep(ctx, "acme", "n1", "copy failed again", 0)
	if err != nil {
		t.Fatalf("last FailMoveStep: %v", err)
	}
	if m.State != MoveStateAborting || m.StepNode != "" || m.StepAttempts != DefaultMoveStepAttempts ||
		m.Error != "copy failed again" || !reflect.DeepEqual(m.NodesPending, []string{"n1", "n2"}) {
		t.Fatalf("move after the last failure = %+v, want aborting with n1 still pending", m)
	}
	var conflict *MoveConflictError
	if _, err := cat.GrantMoveStep(ctx, "acme"); !errors.As(err, &conflict) {
		t.Fatalf("GrantMoveStep on aborting error = %v, want *MoveConflictError", err)
	}
}

func TestCatalog_FailMoveStep_SuccessClearsTheCount(t *testing.T) {
	cat, _, _ := newAdmittedMove(t)
	ctx := context.Background()
	if _, err := cat.GrantMoveStep(ctx, "acme"); err != nil {
		t.Fatalf("GrantMoveStep: %v", err)
	}
	if _, err := cat.FailMoveStep(ctx, "acme", "n1", "copy failed", 0); err != nil {
		t.Fatalf("FailMoveStep: %v", err)
	}
	m, err := cat.CompleteMoveStep(ctx, "acme", "n1")
	if err != nil {
		t.Fatalf("CompleteMoveStep: %v", err)
	}
	if m.StepAttempts != 0 || m.Error != "" {
		t.Fatalf("move = %+v, want attempts and error cleared", m)
	}
}

func TestCatalog_GrantMoveStep_CopiedRefused(t *testing.T) {
	cat, _, _ := newAdmittedMove(t)
	ctx := context.Background()
	for _, n := range []string{"n1", "n2"} {
		if _, err := cat.GrantMoveStep(ctx, "acme"); err != nil {
			t.Fatalf("GrantMoveStep: %v", err)
		}
		if _, err := cat.CompleteMoveStep(ctx, "acme", n); err != nil {
			t.Fatalf("CompleteMoveStep %s: %v", n, err)
		}
	}
	var conflict *MoveConflictError
	if _, err := cat.GrantMoveStep(ctx, "acme"); !errors.As(err, &conflict) {
		t.Fatalf("GrantMoveStep on copied error = %v, want *MoveConflictError", err)
	}
}

func TestCatalog_MoveStep_NoJournalAndCorrupt(t *testing.T) {
	cat, kv, _ := newMoveTestCatalog(t)
	ctx := context.Background()
	var notFound *MoveNotFoundError
	if _, err := cat.GrantMoveStep(ctx, "acme"); !errors.As(err, &notFound) {
		t.Fatalf("GrantMoveStep error = %v, want *MoveNotFoundError", err)
	}
	if _, err := cat.CompleteMoveStep(ctx, "acme", "n1"); !errors.As(err, &notFound) {
		t.Fatalf("CompleteMoveStep error = %v, want *MoveNotFoundError", err)
	}
	if _, err := cat.FailMoveStep(ctx, "acme", "n1", "x", 0); !errors.As(err, &notFound) {
		t.Fatalf("FailMoveStep error = %v, want *MoveNotFoundError", err)
	}
	if err := kv.Put(ctx, MoveKey("acme"), []byte("{not json")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	var corrupt *CorruptRecordError
	if _, err := cat.GrantMoveStep(ctx, "acme"); !errors.As(err, &corrupt) {
		t.Fatalf("GrantMoveStep error = %v, want *CorruptRecordError", err)
	}
	if _, err := cat.CompleteMoveStep(ctx, "acme", "n1"); !errors.As(err, &corrupt) {
		t.Fatalf("CompleteMoveStep error = %v, want *CorruptRecordError", err)
	}
}

func TestCatalog_MoveStep_StoreFailures(t *testing.T) {
	ctx := context.Background()
	t.Run("get", func(t *testing.T) {
		cat, kv, _ := newAdmittedMove(t)
		boom := errors.New("get failed")
		kv.failGet = boom
		if _, err := cat.GrantMoveStep(ctx, "acme"); !errors.Is(err, boom) {
			t.Errorf("GrantMoveStep error = %v, want the get failure", err)
		}
		if _, err := cat.CompleteMoveStep(ctx, "acme", "n1"); !errors.Is(err, boom) {
			t.Errorf("CompleteMoveStep error = %v, want the get failure", err)
		}
		if _, err := cat.FailMoveStep(ctx, "acme", "n1", "x", 0); !errors.Is(err, boom) {
			t.Errorf("FailMoveStep error = %v, want the get failure", err)
		}
	})
	t.Run("execute", func(t *testing.T) {
		cat, kv, tx := newAdmittedMove(t)
		before, err := kv.Get(ctx, MoveKey("acme"))
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		boom := errors.New("execute failed")
		tx.failExecute = boom
		if _, err := cat.GrantMoveStep(ctx, "acme"); !errors.Is(err, boom) {
			t.Fatalf("GrantMoveStep error = %v, want the execute failure", err)
		}
		after, err := kv.Get(ctx, MoveKey("acme"))
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("journal changed by a failed transaction:\n%s\n%s", before, after)
		}
	})
}

func TestCatalog_MoveStep_InvalidRequest(t *testing.T) {
	cat, _, _ := newAdmittedMove(t)
	ctx := context.Background()
	var invalid *InvalidMoveRequestError
	if _, err := cat.GrantMoveStep(ctx, ""); !errors.As(err, &invalid) {
		t.Fatalf("GrantMoveStep(\"\") error = %v, want *InvalidMoveRequestError", err)
	}
	if _, err := cat.CompleteMoveStep(ctx, "acme", ""); !errors.As(err, &invalid) {
		t.Fatalf("CompleteMoveStep empty node error = %v, want *InvalidMoveRequestError", err)
	}
	if _, err := cat.FailMoveStep(ctx, "", "n1", "x", 0); !errors.As(err, &invalid) {
		t.Fatalf("FailMoveStep empty tenant error = %v, want *InvalidMoveRequestError", err)
	}
}

func TestCatalog_MoveStep_BoundedRetries(t *testing.T) {
	cat, kv, tx := newAdmittedMove(t)
	before := mustGet(t, kv, MoveKey("acme"))
	tx.forceFailures = maxCASRetries
	_, err := cat.GrantMoveStep(context.Background(), "acme")
	var target *MoveJournalConflictError
	if !errors.As(err, &target) || target.TenantID != "acme" {
		t.Fatalf("GrantMoveStep under sustained conflict error = %v, want *MoveJournalConflictError", err)
	}
	if after := mustGet(t, kv, MoveKey("acme")); !bytes.Equal(before, after) {
		t.Fatal("journal written although every transaction failed")
	}
}

// copiedMove admits acme's move fast -> bulk and copies both nodes.
func copiedMove(t *testing.T) (*Catalog, *fakeKV, *fakeTx) {
	t.Helper()
	cat, kv, tx := newAdmittedMove(t)
	ctx := context.Background()
	for _, n := range []string{"n1", "n2"} {
		if _, err := cat.GrantMoveStep(ctx, "acme"); err != nil {
			t.Fatalf("GrantMoveStep: %v", err)
		}
		if _, err := cat.CompleteMoveStep(ctx, "acme", n); err != nil {
			t.Fatalf("CompleteMoveStep %s: %v", n, err)
		}
	}
	return cat, kv, tx
}

// cutOverMove is copiedMove cut over with the tenant recorded.
func cutOverMove(t *testing.T) (*Catalog, *fakeKV, *fakeTx) {
	t.Helper()
	cat, kv, tx := copiedMove(t)
	ctx := context.Background()
	if _, err := cat.CutOverMove(ctx, "acme"); err != nil {
		t.Fatalf("CutOverMove: %v", err)
	}
	if _, err := cat.MarkMoveTenantRecorded(ctx, "acme"); err != nil {
		t.Fatalf("MarkMoveTenantRecorded: %v", err)
	}
	return cat, kv, tx
}

func storedAssignment(t *testing.T, kv *fakeKV, tenantID string) Assignment {
	t.Helper()
	var a Assignment
	if err := json.Unmarshal(mustGet(t, kv, AssignmentKey(tenantID)), &a); err != nil {
		t.Fatalf("decode assignment: %v", err)
	}
	return a
}

func reservedOn(t *testing.T, cat *Catalog, name string) int64 {
	t.Helper()
	p, _, err := cat.getPoolRaw(context.Background(), name)
	if err != nil {
		t.Fatalf("getPoolRaw %s: %v", name, err)
	}
	return p.ReservedBytes
}

func wantConflict(t *testing.T, what string, err error) {
	t.Helper()
	var target *MoveConflictError
	if !errors.As(err, &target) {
		t.Fatalf("%s error = %v, want *MoveConflictError", what, err)
	}
}

func TestCatalog_CutOverMove(t *testing.T) {
	cat, kv, _ := copiedMove(t)
	ctx := context.Background()
	before := storedAssignment(t, kv, "acme")
	m, err := cat.CutOverMove(ctx, "acme")
	if err != nil {
		t.Fatalf("CutOverMove: %v", err)
	}
	if m.State != MoveStateCutover || m.SourceReservedBytes != 200 {
		t.Fatalf("move = %+v, want cutover with SourceReservedBytes 200", m)
	}
	got := storedAssignment(t, kv, "acme")
	want := Assignment{TenantID: "acme", Pool: "bulk", Path: before.Path, ReservedBytes: 200}
	if got != want {
		t.Fatalf("assignment = %+v, want %+v", got, want)
	}
	if s := storedMove(t, kv, "acme"); s.State != MoveStateCutover || s.SourceReservedBytes != 200 {
		t.Fatalf("journal = %+v, want cutover", s)
	}
	raw := mustGet(t, kv, MoveKey("acme"))
	again, err := cat.CutOverMove(ctx, "acme")
	if err != nil || again.State != MoveStateCutover {
		t.Fatalf("second CutOverMove = %+v, %v", again, err)
	}
	if !bytes.Equal(raw, mustGet(t, kv, MoveKey("acme"))) {
		t.Fatal("second CutOverMove wrote the journal")
	}
	if reservedOn(t, cat, "fast") != 200 || reservedOn(t, cat, "bulk") != 200 {
		t.Fatal("cut over changed a pool reservation")
	}
}

func TestCatalog_CutOverMove_Refusals(t *testing.T) {
	ctx := context.Background()
	t.Run("admitted", func(t *testing.T) {
		cat, _, _ := newAdmittedMove(t)
		_, err := cat.CutOverMove(ctx, "acme")
		wantConflict(t, "CutOverMove", err)
	})
	t.Run("copying", func(t *testing.T) {
		cat, _, _ := newAdmittedMove(t)
		if _, err := cat.GrantMoveStep(ctx, "acme"); err != nil {
			t.Fatal(err)
		}
		_, err := cat.CutOverMove(ctx, "acme")
		wantConflict(t, "CutOverMove", err)
	})
	t.Run("pending node left", func(t *testing.T) {
		cat, kv, _ := copiedMove(t)
		m := storedMove(t, kv, "acme")
		m.NodesPending = []string{"n3"}
		putMove(t, kv, m)
		_, err := cat.CutOverMove(ctx, "acme")
		wantConflict(t, "CutOverMove", err)
	})
	t.Run("step held", func(t *testing.T) {
		cat, kv, _ := copiedMove(t)
		m := storedMove(t, kv, "acme")
		m.StepNode = "n1"
		putMove(t, kv, m)
		_, err := cat.CutOverMove(ctx, "acme")
		wantConflict(t, "CutOverMove", err)
	})
	t.Run("assignment changed", func(t *testing.T) {
		cat, kv, _ := copiedMove(t)
		a := storedAssignment(t, kv, "acme")
		a.Pool = "other"
		data, _ := json.Marshal(a)
		if err := kv.Put(ctx, AssignmentKey("acme"), data); err != nil {
			t.Fatal(err)
		}
		_, err := cat.CutOverMove(ctx, "acme")
		wantConflict(t, "CutOverMove", err)
	})
	t.Run("assignment missing", func(t *testing.T) {
		cat, kv, _ := copiedMove(t)
		kv.mu.Lock()
		delete(kv.data, AssignmentKey("acme"))
		kv.mu.Unlock()
		var target *AssignmentNotFoundError
		if _, err := cat.CutOverMove(ctx, "acme"); !errors.As(err, &target) {
			t.Fatalf("CutOverMove error = %v, want *AssignmentNotFoundError", err)
		}
	})
	t.Run("corrupt assignment", func(t *testing.T) {
		cat, kv, _ := copiedMove(t)
		if err := kv.Put(ctx, AssignmentKey("acme"), []byte("{not json")); err != nil {
			t.Fatal(err)
		}
		var target *CorruptRecordError
		if _, err := cat.CutOverMove(ctx, "acme"); !errors.As(err, &target) {
			t.Fatalf("CutOverMove error = %v, want *CorruptRecordError", err)
		}
	})
	t.Run("no journal and empty tenant", func(t *testing.T) {
		cat, _, _ := newMoveTestCatalog(t)
		var nf *MoveNotFoundError
		if _, err := cat.CutOverMove(ctx, "acme"); !errors.As(err, &nf) {
			t.Fatalf("CutOverMove error = %v, want *MoveNotFoundError", err)
		}
		var inv *InvalidMoveRequestError
		if _, err := cat.CutOverMove(ctx, ""); !errors.As(err, &inv) {
			t.Fatalf("CutOverMove error = %v, want *InvalidMoveRequestError", err)
		}
	})
	t.Run("store failures", func(t *testing.T) {
		cat, kv, tx := copiedMove(t)
		assignBefore := mustGet(t, kv, AssignmentKey("acme"))
		moveBefore := mustGet(t, kv, MoveKey("acme"))
		boom := errors.New("execute failed")
		tx.failExecute = boom
		if _, err := cat.CutOverMove(ctx, "acme"); !errors.Is(err, boom) {
			t.Fatalf("CutOverMove error = %v, want the execute failure", err)
		}
		if !bytes.Equal(assignBefore, mustGet(t, kv, AssignmentKey("acme"))) || !bytes.Equal(moveBefore, mustGet(t, kv, MoveKey("acme"))) {
			t.Fatal("a failed transaction wrote a record")
		}
		tx.failExecute = nil
		tx.forceFailures = maxCASRetries
		var jc *MoveJournalConflictError
		if _, err := cat.CutOverMove(ctx, "acme"); !errors.As(err, &jc) {
			t.Fatalf("CutOverMove under sustained conflict error = %v, want *MoveJournalConflictError", err)
		}
		getBoom := errors.New("get failed")
		kv.failGet = getBoom
		if _, err := cat.CutOverMove(ctx, "acme"); !errors.Is(err, getBoom) {
			t.Fatalf("CutOverMove error = %v, want the get failure", err)
		}
	})
	t.Run("assignment get failure", func(t *testing.T) {
		cat, kv, _ := copiedMove(t)
		// The journal decodes; the assignment read is the one that fails.
		cat.kv = &getFailAfterKV{fakeKV: kv, key: AssignmentKey("acme"), err: errors.New("assignment get failed")}
		if _, err := cat.CutOverMove(ctx, "acme"); err == nil || err.Error() != "assignment get failed" {
			t.Fatalf("CutOverMove error = %v, want the assignment get failure", err)
		}
	})
}

// getFailAfterKV fails the Get of one key and serves the rest from fakeKV.
type getFailAfterKV struct {
	*fakeKV
	key string
	err error
}

func (g *getFailAfterKV) Get(ctx context.Context, key string) ([]byte, error) {
	if key == g.key {
		return nil, g.err
	}
	return g.fakeKV.Get(ctx, key)
}

func putMove(t *testing.T, kv *fakeKV, m *Move) {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := kv.Put(context.Background(), MoveKey(m.TenantID), data); err != nil {
		t.Fatal(err)
	}
}

func TestCatalog_MarkMoveTenantRecorded(t *testing.T) {
	ctx := context.Background()
	cat, kv, _ := copiedMove(t)
	_, err := cat.MarkMoveTenantRecorded(ctx, "acme")
	wantConflict(t, "MarkMoveTenantRecorded on copied", err)
	if _, err := cat.CutOverMove(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	m, err := cat.MarkMoveTenantRecorded(ctx, "acme")
	if err != nil || !m.TenantRecorded || !storedMove(t, kv, "acme").TenantRecorded {
		t.Fatalf("MarkMoveTenantRecorded = %+v, %v", m, err)
	}
}

func TestCatalog_RetireSteps(t *testing.T) {
	cat, kv, _ := cutOverMove(t)
	ctx := context.Background()
	for i, n := range []string{"n1", "n2"} {
		m, err := cat.GrantRetireStep(ctx, "acme")
		if err != nil || m.StepNode != n {
			t.Fatalf("GrantRetireStep %d = %+v, %v, want %s", i, m, err, n)
		}
		// A granted step is returned again with no write.
		if again, err := cat.GrantRetireStep(ctx, "acme"); err != nil || again.StepNode != n {
			t.Fatalf("second GrantRetireStep = %+v, %v", again, err)
		}
		m, err = cat.CompleteRetireStep(ctx, "acme", n)
		if err != nil {
			t.Fatalf("CompleteRetireStep %s: %v", n, err)
		}
		if i == 0 {
			if s := storedMove(t, kv, "acme"); s == nil || !reflect.DeepEqual(s.NodesRetired, []string{"n1"}) || s.StepNode != "" {
				t.Fatalf("journal after n1 = %+v", s)
			}
			if reservedOn(t, cat, "fast") != 200 {
				t.Fatal("source released before every node retired")
			}
		} else if !reflect.DeepEqual(m.NodesRetired, []string{"n1", "n2"}) {
			t.Fatalf("final move = %+v", m)
		}
	}
	if s := storedMove(t, kv, "acme"); s != nil {
		t.Fatalf("journal = %+v, want deleted", s)
	}
	if got := reservedOn(t, cat, "fast"); got != 0 {
		t.Fatalf("fast reserved = %d, want 0", got)
	}
	if got := reservedOn(t, cat, "bulk"); got != 200 {
		t.Fatalf("bulk reserved = %d, want 200", got)
	}
	if err := cat.Release(ctx, "acme"); err != nil {
		t.Fatalf("Release after the move: %v", err)
	}
	if got := reservedOn(t, cat, "bulk"); got != 0 {
		t.Fatalf("bulk reserved after Release = %d, want 0", got)
	}
}

func TestCatalog_RetireStep_SourceFloorZero(t *testing.T) {
	cat, kv, _ := cutOverMove(t)
	ctx := context.Background()
	m := storedMove(t, kv, "acme")
	m.SourceReservedBytes = 5000
	putMove(t, kv, m)
	for _, n := range []string{"n1", "n2"} {
		if _, err := cat.GrantRetireStep(ctx, "acme"); err != nil {
			t.Fatal(err)
		}
		if _, err := cat.CompleteRetireStep(ctx, "acme", n); err != nil {
			t.Fatal(err)
		}
	}
	if got := reservedOn(t, cat, "fast"); got != 0 {
		t.Fatalf("fast reserved = %d, want floor 0", got)
	}
}

func TestCatalog_RetireStep_NeedsTenantRecorded(t *testing.T) {
	cat, _, _ := copiedMove(t)
	ctx := context.Background()
	_, err := cat.GrantRetireStep(ctx, "acme")
	wantConflict(t, "GrantRetireStep on copied", err)
	if _, err := cat.CutOverMove(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	_, err = cat.GrantRetireStep(ctx, "acme")
	wantConflict(t, "GrantRetireStep before the tenant is recorded", err)
}

func TestCatalog_RetireStep_FailureStaysCutover(t *testing.T) {
	cat, kv, _ := cutOverMove(t)
	ctx := context.Background()
	if _, err := cat.GrantRetireStep(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= DefaultMoveStepAttempts+2; i++ {
		m, err := cat.FailRetireStep(ctx, "acme", "n1", "disk busy")
		if err != nil {
			t.Fatalf("FailRetireStep %d: %v", i, err)
		}
		if m.State != MoveStateCutover || m.StepNode != "n1" || m.StepAttempts != i || m.Error != "disk busy" {
			t.Fatalf("move after %d failures = %+v", i, m)
		}
	}
	_, err := cat.FailRetireStep(ctx, "acme", "n2", "x")
	wantConflict(t, "FailRetireStep by a node without the step", err)
	_, err = cat.CompleteRetireStep(ctx, "acme", "n2")
	wantConflict(t, "CompleteRetireStep by a node without the step", err)
	if _, err := cat.CompleteRetireStep(ctx, "acme", "n1"); err != nil {
		t.Fatal(err)
	}
	if s := storedMove(t, kv, "acme"); s.StepAttempts != 0 || s.Error != "" {
		t.Fatalf("journal after success = %+v, want the count cleared", s)
	}
	m := storedMove(t, kv, "acme")
	m.NodesRetired = append(m.NodesRetired, "n2")
	putMove(t, kv, m)
	_, err = cat.GrantRetireStep(ctx, "acme")
	wantConflict(t, "GrantRetireStep with every node retired", err)
}

func TestCatalog_CancelMove(t *testing.T) {
	ctx := context.Background()
	setups := map[string]func(t *testing.T) (*Catalog, *fakeKV, *fakeTx){
		"admitted": newAdmittedMove,
		"copying": func(t *testing.T) (*Catalog, *fakeKV, *fakeTx) {
			cat, kv, tx := newAdmittedMove(t)
			if _, err := cat.GrantMoveStep(ctx, "acme"); err != nil {
				t.Fatal(err)
			}
			if _, err := cat.CompleteMoveStep(ctx, "acme", "n1"); err != nil {
				t.Fatal(err)
			}
			return cat, kv, tx
		},
		"copied": copiedMove,
	}
	for name, setup := range setups {
		t.Run(name, func(t *testing.T) {
			cat, kv, _ := setup(t)
			m, err := cat.CancelMove(ctx, "acme")
			if err != nil {
				t.Fatalf("CancelMove: %v", err)
			}
			if m.State != MoveStateAborting || m.StepNode != "" || m.StepAttempts != 0 || m.Error != "cancelled by operator" {
				t.Fatalf("move = %+v, want aborting, step released", m)
			}
			raw := mustGet(t, kv, MoveKey("acme"))
			if again, err := cat.CancelMove(ctx, "acme"); err != nil || again.State != MoveStateAborting {
				t.Fatalf("second CancelMove = %+v, %v", again, err)
			}
			if !bytes.Equal(raw, mustGet(t, kv, MoveKey("acme"))) {
				t.Fatal("cancelling an aborting move wrote the journal")
			}
		})
	}
	t.Run("cutover", func(t *testing.T) {
		cat, _, _ := cutOverMove(t)
		_, err := cat.CancelMove(ctx, "acme")
		wantConflict(t, "CancelMove on cutover", err)
	})
	t.Run("missing and empty", func(t *testing.T) {
		cat, _, _ := newMoveTestCatalog(t)
		var nf *MoveNotFoundError
		if _, err := cat.CancelMove(ctx, "acme"); !errors.As(err, &nf) {
			t.Fatalf("CancelMove error = %v, want *MoveNotFoundError", err)
		}
		var inv *InvalidMoveRequestError
		if _, err := cat.CancelMove(ctx, ""); !errors.As(err, &inv) {
			t.Fatalf("CancelMove error = %v, want *InvalidMoveRequestError", err)
		}
	})
	t.Run("store failures", func(t *testing.T) {
		cat, kv, tx := newAdmittedMove(t)
		before := mustGet(t, kv, MoveKey("acme"))
		boom := errors.New("execute failed")
		tx.failExecute = boom
		if _, err := cat.CancelMove(ctx, "acme"); !errors.Is(err, boom) {
			t.Fatalf("CancelMove error = %v, want the execute failure", err)
		}
		if !bytes.Equal(before, mustGet(t, kv, MoveKey("acme"))) {
			t.Fatal("a failed transaction wrote the journal")
		}
		getBoom := errors.New("get failed")
		kv.failGet = getBoom
		if _, err := cat.CancelMove(ctx, "acme"); !errors.Is(err, getBoom) {
			t.Fatalf("CancelMove error = %v, want the get failure", err)
		}
	})
}

func TestCatalog_CancelMove_AwaitsRunningCopy(t *testing.T) {
	ctx := context.Background()
	cancelRunning := func(t *testing.T) (*Catalog, *fakeKV) {
		t.Helper()
		cat, kv, _ := newAdmittedMove(t)
		if _, err := cat.GrantMoveStep(ctx, "acme"); err != nil {
			t.Fatal(err)
		}
		m, err := cat.CancelMove(ctx, "acme")
		if err != nil {
			t.Fatal(err)
		}
		if m.State != MoveStateCopying || m.StepNode != "n1" || !m.CancelRequested || m.Error != "cancelled by operator" {
			t.Fatalf("CancelMove = %+v, want copying, n1 still holding, cancel requested", m)
		}
		raw := mustGet(t, kv, MoveKey("acme"))
		if again, err := cat.CancelMove(ctx, "acme"); err != nil || !again.CancelRequested {
			t.Fatalf("second CancelMove = %+v, %v", again, err)
		}
		if !bytes.Equal(raw, mustGet(t, kv, MoveKey("acme"))) {
			t.Fatal("cancelling a cancelling move wrote the journal")
		}
		return cat, kv
	}
	t.Run("copy completes", func(t *testing.T) {
		cat, kv := cancelRunning(t)
		if _, err := cat.CompleteMoveStep(ctx, "acme", "n1"); err != nil {
			t.Fatalf("CompleteMoveStep after cancel: %v", err)
		}
		s := storedMove(t, kv, "acme")
		if s.State != MoveStateAborting || s.StepNode != "" || s.CancelRequested ||
			!reflect.DeepEqual(s.NodesCopied, []string{"n1"}) || !reflect.DeepEqual(s.NodesPending, []string{"n2"}) {
			t.Fatalf("journal = %+v, want aborting with n1 copied", s)
		}
		m, err := cat.GrantAbortStep(ctx, "acme")
		if err != nil || m.StepNode != "n1" {
			t.Fatalf("GrantAbortStep = %+v, %v; want n1 (copied, copies back)", m, err)
		}
	})
	t.Run("copy fails", func(t *testing.T) {
		cat, kv := cancelRunning(t)
		if _, err := cat.FailMoveStep(ctx, "acme", "n1", "x", 0); err != nil {
			t.Fatalf("FailMoveStep after cancel: %v", err)
		}
		s := storedMove(t, kv, "acme")
		if s.State != MoveStateAborting || s.StepNode != "" || s.CancelRequested || len(s.NodesCopied) != 0 ||
			!reflect.DeepEqual(s.NodesPending, []string{"n1", "n2"}) || s.Error != "cancelled by operator" {
			t.Fatalf("journal = %+v, want aborting with both nodes pending", s)
		}
	})
}

func TestCatalog_AbortSteps(t *testing.T) {
	cat, kv, _ := newAdmittedMove(t)
	ctx := context.Background()
	if _, err := cat.GrantMoveStep(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CompleteMoveStep(ctx, "acme", "n1"); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CancelMove(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	assignBefore := mustGet(t, kv, AssignmentKey("acme"))
	for i, n := range []string{"n1", "n2"} {
		m, err := cat.GrantAbortStep(ctx, "acme")
		if err != nil || m.StepNode != n {
			t.Fatalf("GrantAbortStep %d = %+v, %v, want %s", i, m, err, n)
		}
		if again, err := cat.GrantAbortStep(ctx, "acme"); err != nil || again.StepNode != n {
			t.Fatalf("second GrantAbortStep = %+v, %v", again, err)
		}
		if _, err := cat.CompleteAbortStep(ctx, "acme", n); err != nil {
			t.Fatalf("CompleteAbortStep %s: %v", n, err)
		}
		if i == 0 {
			s := storedMove(t, kv, "acme")
			if len(s.NodesCopied) != 0 || !reflect.DeepEqual(s.NodesPending, []string{"n2"}) {
				t.Fatalf("journal after n1 = %+v", s)
			}
			if reservedOn(t, cat, "bulk") != 200 {
				t.Fatal("destination released before every node aborted")
			}
		}
	}
	if s := storedMove(t, kv, "acme"); s != nil {
		t.Fatalf("journal = %+v, want deleted", s)
	}
	if !bytes.Equal(assignBefore, mustGet(t, kv, AssignmentKey("acme"))) {
		t.Fatal("abort wrote the assignment")
	}
	if got := reservedOn(t, cat, "bulk"); got != 0 {
		t.Fatalf("bulk reserved = %d, want 0", got)
	}
	if got := reservedOn(t, cat, "fast"); got != 200 {
		t.Fatalf("fast reserved = %d, want 200", got)
	}
}

func TestCatalog_ResolveForNode(t *testing.T) {
	cat, _, _ := newAdmittedMove(t)
	ctx := context.Background()
	pool := func(node string) string {
		t.Helper()
		a, err := cat.ResolveForNode(ctx, "acme", node)
		if err != nil {
			t.Fatalf("ResolveForNode(%s): %v", node, err)
		}
		return a.Pool
	}
	if got := pool("n2"); got != "fast" {
		t.Fatalf("admitted: n1 pool = %s, want the source", got)
	}
	if _, err := cat.GrantMoveStep(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CompleteMoveStep(ctx, "acme", "n1"); err != nil {
		t.Fatal(err)
	}
	if got := pool("n1"); got != "bulk" {
		t.Fatalf("copied node n1 pool = %s, want the destination", got)
	}
	for _, n := range []string{"n2", ""} {
		if got := pool(n); got != "fast" {
			t.Fatalf("node %q pool = %s, want the source", n, got)
		}
	}
	if _, err := cat.CancelMove(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if got := pool("n1"); got != "bulk" {
		t.Fatalf("aborting, before n1's abort step: pool = %s, want the destination", got)
	}
	if _, err := cat.GrantAbortStep(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CompleteAbortStep(ctx, "acme", "n1"); err != nil {
		t.Fatal(err)
	}
	if got := pool("n1"); got != "fast" {
		t.Fatalf("after n1's abort step: pool = %s, want the source", got)
	}
	if _, err := cat.ResolveForNode(ctx, "nobody", "n2"); err == nil {
		t.Fatal("unknown tenant resolved")
	}
}

func TestCatalog_ResolveForNode_CutOverNamesTheDestination(t *testing.T) {
	cat, _, _ := copiedMove(t)
	ctx := context.Background()
	if _, err := cat.CutOverMove(ctx, "acme"); err != nil {
		t.Fatalf("CutOverMove: %v", err)
	}
	for _, n := range []string{"n1", "n2", "n3"} {
		a, err := cat.ResolveForNode(ctx, "acme", n)
		if err != nil || a.Pool != "bulk" {
			t.Fatalf("node %s = %+v, %v; want bulk", n, a, err)
		}
	}
}

func TestCatalog_AbortStep_Refusals(t *testing.T) {
	ctx := context.Background()
	cat, kv, _ := newAdmittedMove(t)
	_, err := cat.GrantAbortStep(ctx, "acme")
	wantConflict(t, "GrantAbortStep on admitted", err)
	if _, err := cat.CancelMove(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	m := storedMove(t, kv, "acme")
	m.NodesPending = nil
	putMove(t, kv, m)
	_, err = cat.GrantAbortStep(ctx, "acme")
	wantConflict(t, "GrantAbortStep with no node left", err)
	var inv *InvalidMoveRequestError
	if _, err := cat.CompleteAbortStep(ctx, "acme", ""); !errors.As(err, &inv) {
		t.Fatalf("CompleteAbortStep error = %v, want *InvalidMoveRequestError", err)
	}
	if _, err := cat.FailAbortStep(ctx, "acme", "", "x"); !errors.As(err, &inv) {
		t.Fatalf("FailAbortStep error = %v, want *InvalidMoveRequestError", err)
	}
	if _, err := cat.CompleteRetireStep(ctx, "acme", ""); !errors.As(err, &inv) {
		t.Fatalf("CompleteRetireStep error = %v, want *InvalidMoveRequestError", err)
	}
	if _, err := cat.GrantAbortStep(ctx, ""); !errors.As(err, &inv) {
		t.Fatalf("GrantAbortStep error = %v, want *InvalidMoveRequestError", err)
	}
	var nf *MoveNotFoundError
	if _, err := cat.GrantRetireStep(ctx, "nobody"); !errors.As(err, &nf) {
		t.Fatalf("GrantRetireStep error = %v, want *MoveNotFoundError", err)
	}
}

func TestCatalog_AbortStep_FailureKeepsTheNode(t *testing.T) {
	cat, kv, _ := copiedMove(t)
	ctx := context.Background()
	if _, err := cat.CancelMove(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.GrantAbortStep(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= DefaultMoveStepAttempts+2; i++ {
		m, err := cat.FailAbortStep(ctx, "acme", "n1", "remove failed")
		if err != nil {
			t.Fatalf("FailAbortStep %d: %v", i, err)
		}
		if m.State != MoveStateAborting || m.StepNode != "n1" || m.StepAttempts != i ||
			!reflect.DeepEqual(m.NodesCopied, []string{"n1", "n2"}) {
			t.Fatalf("move after %d failures = %+v", i, m)
		}
	}
	if s := storedMove(t, kv, "acme"); s.Error != "remove failed" {
		t.Fatalf("journal = %+v, want the failure recorded", s)
	}
	_, err := cat.CompleteAbortStep(ctx, "acme", "n2")
	wantConflict(t, "CompleteAbortStep by a node without the step", err)
}

func TestCatalog_PhaseSteps_StoreFailures(t *testing.T) {
	ctx := context.Background()
	t.Run("get", func(t *testing.T) {
		cat, kv, _ := cutOverMove(t)
		boom := errors.New("get failed")
		kv.failGet = boom
		calls := map[string]func() error{
			"GrantRetireStep":        func() error { _, err := cat.GrantRetireStep(ctx, "acme"); return err },
			"CompleteRetireStep":     func() error { _, err := cat.CompleteRetireStep(ctx, "acme", "n1"); return err },
			"FailRetireStep":         func() error { _, err := cat.FailRetireStep(ctx, "acme", "n1", "x"); return err },
			"GrantAbortStep":         func() error { _, err := cat.GrantAbortStep(ctx, "acme"); return err },
			"CompleteAbortStep":      func() error { _, err := cat.CompleteAbortStep(ctx, "acme", "n1"); return err },
			"FailAbortStep":          func() error { _, err := cat.FailAbortStep(ctx, "acme", "n1", "x"); return err },
			"MarkMoveTenantRecorded": func() error { _, err := cat.MarkMoveTenantRecorded(ctx, "acme"); return err },
		}
		for name, call := range calls {
			if err := call(); !errors.Is(err, boom) {
				t.Errorf("%s error = %v, want the get failure", name, err)
			}
		}
	})
	t.Run("execute", func(t *testing.T) {
		for _, phase := range []string{MoveStepPhaseRetire, MoveStepPhaseAbort} {
			var cat *Catalog
			var kv *fakeKV
			var tx *fakeTx
			grant, complete := (*Catalog).GrantRetireStep, (*Catalog).CompleteRetireStep
			if phase == MoveStepPhaseRetire {
				cat, kv, tx = cutOverMove(t)
			} else {
				cat, kv, tx = copiedMove(t)
				if _, err := cat.CancelMove(ctx, "acme"); err != nil {
					t.Fatal(err)
				}
				grant, complete = (*Catalog).GrantAbortStep, (*Catalog).CompleteAbortStep
			}
			before := mustGet(t, kv, MoveKey("acme"))
			boom := errors.New("execute failed")
			tx.failExecute = boom
			if _, err := grant(cat, ctx, "acme"); !errors.Is(err, boom) {
				t.Fatalf("%s grant error = %v, want the execute failure", phase, err)
			}
			if !bytes.Equal(before, mustGet(t, kv, MoveKey("acme"))) {
				t.Fatalf("%s: a failed transaction wrote the journal", phase)
			}
			// The final step's release transaction fails: nothing is written.
			tx.failExecute = nil
			for _, n := range []string{"n1", "n2"} {
				if _, err := grant(cat, ctx, "acme"); err != nil {
					t.Fatal(err)
				}
				if n == "n2" {
					break
				}
				if _, err := complete(cat, ctx, "acme", n); err != nil {
					t.Fatal(err)
				}
			}
			before = mustGet(t, kv, MoveKey("acme"))
			tx.failExecute = boom
			if _, err := complete(cat, ctx, "acme", "n2"); !errors.Is(err, boom) {
				t.Fatalf("%s final complete error = %v, want the execute failure", phase, err)
			}
			if !bytes.Equal(before, mustGet(t, kv, MoveKey("acme"))) {
				t.Fatalf("%s: a failed release wrote the journal", phase)
			}
			tx.failExecute = nil
			// The released pool is gone: the final step returns its error.
			kv.mu.Lock()
			delete(kv.data, PoolKey("fast"))
			delete(kv.data, PoolKey("bulk"))
			kv.mu.Unlock()
			var pnf *PoolNotFoundError
			if _, err := complete(cat, ctx, "acme", "n2"); !errors.As(err, &pnf) {
				t.Fatalf("%s final complete error = %v, want *PoolNotFoundError", phase, err)
			}
		}
	})
}

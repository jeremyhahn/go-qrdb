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

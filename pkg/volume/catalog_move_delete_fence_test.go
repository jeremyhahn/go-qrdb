package volume

import (
	"context"
	"errors"
	"testing"
)

// A fenced delete refuses every later admission.
func TestCatalog_FenceTenantDelete_RefusesLaterAdmit(t *testing.T) {
	cat, kv, _ := newMoveTestCatalog(t)
	ctx := context.Background()
	if err := cat.FenceTenantDelete(ctx, "acme"); err != nil {
		t.Fatalf("FenceTenantDelete: %v", err)
	}
	if !storedAssignment(t, kv, "acme").Retiring {
		t.Fatal("assignment not marked retiring")
	}
	// Fencing twice is a no-op success.
	if err := cat.FenceTenantDelete(ctx, "acme"); err != nil {
		t.Fatalf("second FenceTenantDelete: %v", err)
	}
	_, err := cat.AdmitMove(ctx, admitAcme())
	var conflict *MoveConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("AdmitMove after the fence error = %v, want *MoveConflictError", err)
	}
	if storedMove(t, kv, "acme") != nil {
		t.Fatal("journal written for a retiring tenant")
	}
	if got := poolReserved(t, kv, "bulk"); got != 0 {
		t.Fatalf("bulk reserved = %d, want 0", got)
	}

	// A delete that failed lifts the fence and the move admits again.
	if err := cat.UnfenceTenantDelete(ctx, "acme"); err != nil {
		t.Fatalf("UnfenceTenantDelete: %v", err)
	}
	if storedAssignment(t, kv, "acme").Retiring {
		t.Fatal("assignment still retiring after unfence")
	}
	if _, err := cat.AdmitMove(ctx, admitAcme()); err != nil {
		t.Fatalf("AdmitMove after unfence: %v", err)
	}
}

// A running move refuses the fence, and the assignment is left untouched.
func TestCatalog_FenceTenantDelete_RefusedWhileMoving(t *testing.T) {
	cat, kv, _ := newMoveTestCatalog(t)
	ctx := context.Background()
	if _, err := cat.AdmitMove(ctx, admitAcme()); err != nil {
		t.Fatalf("AdmitMove: %v", err)
	}
	err := cat.FenceTenantDelete(ctx, "acme")
	var conflict *MoveConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("FenceTenantDelete while moving error = %v, want *MoveConflictError", err)
	}
	if storedAssignment(t, kv, "acme").Retiring {
		t.Fatal("assignment marked retiring although a move runs")
	}
}

// The race the fence closes: a delete fences after AdmitMove read the
// assignment and before its transaction commits. The admission's compare
// on the assignment fails, its retry reads the retiring mark, and no
// journal is written.
func TestCatalog_FenceTenantDelete_WinsRaceWithAdmit(t *testing.T) {
	cat, kv, tx := newMoveTestCatalog(t)
	ctx := context.Background()
	var fenceErr error
	tx.beforeExecute = func() { fenceErr = cat.FenceTenantDelete(ctx, "acme") }
	_, err := cat.AdmitMove(ctx, admitAcme())
	if fenceErr != nil {
		t.Fatalf("interleaved FenceTenantDelete: %v", fenceErr)
	}
	var conflict *MoveConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("AdmitMove racing the fence error = %v, want *MoveConflictError", err)
	}
	if storedMove(t, kv, "acme") != nil {
		t.Fatal("journal written although the delete fenced first")
	}
	if got := poolReserved(t, kv, "bulk"); got != 0 {
		t.Fatalf("bulk reserved = %d, want 0", got)
	}
}

// The reverse order: a move commits between the fence's reads and its
// transaction. The fence's version compare on the journal fails, its
// retry reads the journal, and the delete is refused.
func TestCatalog_FenceTenantDelete_LosesRaceWithAdmit(t *testing.T) {
	cat, kv, tx := newMoveTestCatalog(t)
	ctx := context.Background()
	var admitErr error
	tx.beforeExecute = func() { _, admitErr = cat.AdmitMove(ctx, admitAcme()) }
	err := cat.FenceTenantDelete(ctx, "acme")
	if admitErr != nil {
		t.Fatalf("interleaved AdmitMove: %v", admitErr)
	}
	var conflict *MoveConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("FenceTenantDelete racing an admit error = %v, want *MoveConflictError", err)
	}
	if storedAssignment(t, kv, "acme").Retiring {
		t.Fatal("assignment marked retiring although the move admitted first")
	}
	if storedMove(t, kv, "acme") == nil {
		t.Fatal("the admitted move's journal is gone")
	}
}

func TestCatalog_FenceTenantDelete_NoAssignment(t *testing.T) {
	cat, _, _ := newMoveTestCatalog(t)
	if err := cat.FenceTenantDelete(context.Background(), "nobody"); err != nil {
		t.Fatalf("FenceTenantDelete without an assignment: %v", err)
	}
	if err := cat.UnfenceTenantDelete(context.Background(), "nobody"); err != nil {
		t.Fatalf("UnfenceTenantDelete without an assignment: %v", err)
	}
}

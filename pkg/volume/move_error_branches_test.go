package volume

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
	"testing"
)

func TestMoveErrors_MessagesNameTheSubject(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want []string
	}{
		{"TenantMovingReplicaError", &TenantMovingReplicaError{TenantID: "acme", FromPool: "fast", ToPool: "bulk", State: "copying"}, []string{`"acme"`, `"fast"`, `"bulk"`, "copying", "replica set"}},
		{"TenantSealedError", &TenantSealedError{TenantID: "acme"}, []string{"acme", "sealed"}},
		{"ResolveInconsistentError", &ResolveInconsistentError{TenantID: "acme", Attempts: 3}, []string{`"acme"`, "3"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := c.err.Error()
			for _, want := range c.want {
				if !strings.Contains(msg, want) {
					t.Fatalf("Error() = %q, want it to contain %q", msg, want)
				}
			}
		})
	}
}

func TestTenantMovingReplicaError_UnwrapsToMoveConflict(t *testing.T) {
	err := error(&TenantMovingReplicaError{TenantID: "acme", FromPool: "fast", ToPool: "bulk", State: "admitted"})
	var conflict *MoveConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("errors.As(%v, *MoveConflictError) = false", err)
	}
	if conflict.TenantID != "acme" || !strings.Contains(conflict.Reason, "admitted") {
		t.Fatalf("conflict = %+v, want tenant acme and a reason naming the state", conflict)
	}
}

func TestRefuseReplicaChangeIfMoving(t *testing.T) {
	ctx := context.Background()

	t.Run("no move", func(t *testing.T) {
		cat, _, _ := newMoveTestCatalog(t)
		if err := RefuseReplicaChangeIfMoving(ctx, cat, "acme"); err != nil {
			t.Fatalf("RefuseReplicaChangeIfMoving = %v, want nil", err)
		}
	})

	t.Run("move in flight", func(t *testing.T) {
		cat, _, _ := newMoveTestCatalog(t)
		if _, err := cat.AdmitMove(ctx, admitAcme()); err != nil {
			t.Fatalf("AdmitMove: %v", err)
		}
		err := RefuseReplicaChangeIfMoving(ctx, cat, "acme")
		var moving *TenantMovingReplicaError
		if !errors.As(err, &moving) {
			t.Fatalf("RefuseReplicaChangeIfMoving = %v, want *TenantMovingReplicaError", err)
		}
		if moving.TenantID != "acme" || moving.FromPool != "fast" || moving.ToPool != "bulk" || moving.State != "admitted" {
			t.Fatalf("refusal = %+v", moving)
		}
	})

	t.Run("lookup failure passes through", func(t *testing.T) {
		cat, _, _ := newMoveTestCatalog(t)
		var invalid *InvalidMoveRequestError
		if err := RefuseReplicaChangeIfMoving(ctx, cat, ""); !errors.As(err, &invalid) {
			t.Fatalf("RefuseReplicaChangeIfMoving(\"\") = %v, want *InvalidMoveRequestError", err)
		}
	})

	t.Run("corrupt journal fails closed", func(t *testing.T) {
		cat, kv, _ := newMoveTestCatalog(t)
		if err := kv.Put(ctx, MoveKey("acme"), []byte("{not json")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		var corrupt *CorruptRecordError
		if err := RefuseReplicaChangeIfMoving(ctx, cat, "acme"); !errors.As(err, &corrupt) {
			t.Fatalf("RefuseReplicaChangeIfMoving = %v, want *CorruptRecordError", err)
		}
	})
}

func TestMoveStepErrorClass_EveryClass(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{context.DeadlineExceeded, "timed out"},
		{fmt.Errorf("copy: %w", context.Canceled), "cancelled"},
		{&ManifestMismatchError{}, "manifest mismatch"},
		{&PoolUnavailableError{Pool: "bulk"}, "pool unavailable"},
		{&NodeSealedError{}, "node sealed"},
		{&TenantSealedError{TenantID: "acme"}, "tenant sealed"},
		{&ClosedVolumeError{Pool: "bulk"}, "volume closed"},
		{&OpenError{Pool: "bulk", Err: errors.New("boom")}, "volume open failed"},
		{fmt.Errorf("walk: %w", &notADirectoryError{}), "path is not a directory"},
		{&unsupportedEntryError{Kind: "socket"}, "unsupported directory entry"},
		{fmt.Errorf("write: %w", syscall.ENOSPC), "no space left on the pool"},
		{fmt.Errorf("open: %w", fs.ErrPermission), "permission denied"},
		{fmt.Errorf("stat: %w", fs.ErrNotExist), "path does not exist"},
		{fmt.Errorf("mkdir: %w", fs.ErrExist), "path already exists"},
		{errors.New("something else"), "internal error (see the node log)"},
	}
	for _, c := range cases {
		if got := moveStepErrorClass(c.err); got != c.want {
			t.Errorf("moveStepErrorClass(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

func TestCatalog_ResolveForNode_Refusals(t *testing.T) {
	ctx := context.Background()

	t.Run("store failure", func(t *testing.T) {
		cat, kv, _ := newMoveTestCatalog(t)
		boom := errors.New("store down")
		kv.mu.Lock()
		kv.failGet = boom
		kv.mu.Unlock()
		if _, err := cat.ResolveForNode(ctx, "acme", "n1"); !errors.Is(err, boom) {
			t.Fatalf("ResolveForNode = %v, want the store error", err)
		}
	})

	t.Run("no assignment", func(t *testing.T) {
		cat, _, _ := newMoveTestCatalog(t)
		var notFound *AssignmentNotFoundError
		if _, err := cat.ResolveForNode(ctx, "nobody", "n1"); !errors.As(err, &notFound) || notFound.TenantID != "nobody" {
			t.Fatalf("ResolveForNode = %v, want *AssignmentNotFoundError for nobody", err)
		}
	})

	t.Run("corrupt assignment", func(t *testing.T) {
		cat, kv, _ := newMoveTestCatalog(t)
		if err := kv.Put(ctx, AssignmentKey("acme"), []byte("{not json")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		var corrupt *CorruptRecordError
		if _, err := cat.ResolveForNode(ctx, "acme", "n1"); !errors.As(err, &corrupt) {
			t.Fatalf("ResolveForNode = %v, want *CorruptRecordError", err)
		}
	})

	t.Run("escaping path", func(t *testing.T) {
		cat, kv, _ := newMoveTestCatalog(t)
		if err := kv.Put(ctx, AssignmentKey("acme"), []byte(`{"TenantID":"acme","Pool":"fast","Path":"../other"}`)); err != nil {
			t.Fatalf("Put: %v", err)
		}
		var badPath *InvalidAssignmentPathError
		if _, err := cat.ResolveForNode(ctx, "acme", "n1"); !errors.As(err, &badPath) {
			t.Fatalf("ResolveForNode = %v, want *InvalidAssignmentPathError", err)
		}
	})

	t.Run("corrupt journal", func(t *testing.T) {
		cat, kv, _ := newMoveTestCatalog(t)
		if err := kv.Put(ctx, MoveKey("acme"), []byte("{not json")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		var corrupt *CorruptRecordError
		if _, err := cat.ResolveForNode(ctx, "acme", "n1"); !errors.As(err, &corrupt) {
			t.Fatalf("ResolveForNode = %v, want *CorruptRecordError", err)
		}
	})
}

func TestMoveReconciler_CopyRecorded(t *testing.T) {
	ctx := context.Background()
	cat, kv, _ := newMoveTestCatalog(t)
	if _, err := cat.AdmitMove(ctx, admitAcme()); err != nil {
		t.Fatalf("AdmitMove: %v", err)
	}
	r := &MoveReconciler{cat: cat}

	if r.copyRecorded(ctx, "acme", MoveStep{TenantID: "acme", NodeID: "n1", Phase: MoveStepPhaseAbort}) {
		t.Fatal("copyRecorded(abort step) = true, want false: only a copy step is recorded")
	}
	if r.copyRecorded(ctx, "acme", MoveStep{TenantID: "acme", NodeID: "n1", Phase: MoveStepPhaseCopy}) {
		t.Fatal("copyRecorded(copy n1) = true before the journal names n1")
	}

	rewriteRecord(t, kv, MoveKey("acme"), func(m *Move) { m.NodesCopied = []string{"n1"} })
	if !r.copyRecorded(ctx, "acme", MoveStep{TenantID: "acme", NodeID: "n1", Phase: MoveStepPhaseCopy}) {
		t.Fatal("copyRecorded(copy n1) = false, want true once the journal names n1")
	}
	if r.copyRecorded(ctx, "acme", MoveStep{TenantID: "acme", NodeID: "n2", Phase: MoveStepPhaseCopy}) {
		t.Fatal("copyRecorded(copy n2) = true, the journal names only n1")
	}

	if err := kv.Put(ctx, MoveKey("acme"), []byte("{not json")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if r.copyRecorded(ctx, "acme", MoveStep{TenantID: "acme", NodeID: "n1", Phase: MoveStepPhaseCopy}) {
		t.Fatal("copyRecorded on a corrupt journal = true, want false (fail closed)")
	}
}

func TestCatalog_MoveDropsEncryption_Refusals(t *testing.T) {
	ctx := context.Background()

	t.Run("store failure", func(t *testing.T) {
		cat, kv, _ := newMoveTestCatalog(t)
		boom := errors.New("store down")
		kv.mu.Lock()
		kv.failGet = boom
		kv.mu.Unlock()
		if _, _, err := cat.moveDropsEncryption(ctx, "acme", "bulk"); !errors.Is(err, boom) {
			t.Fatalf("moveDropsEncryption = %v, want the store error", err)
		}
	})

	t.Run("no assignment", func(t *testing.T) {
		cat, _, _ := newMoveTestCatalog(t)
		var notFound *AssignmentNotFoundError
		if _, _, err := cat.moveDropsEncryption(ctx, "nobody", "bulk"); !errors.As(err, &notFound) {
			t.Fatalf("moveDropsEncryption = %v, want *AssignmentNotFoundError", err)
		}
	})

	t.Run("corrupt assignment", func(t *testing.T) {
		cat, kv, _ := newMoveTestCatalog(t)
		if err := kv.Put(ctx, AssignmentKey("acme"), []byte("{not json")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		var corrupt *CorruptRecordError
		if _, _, err := cat.moveDropsEncryption(ctx, "acme", "bulk"); !errors.As(err, &corrupt) {
			t.Fatalf("moveDropsEncryption = %v, want *CorruptRecordError", err)
		}
	})

	t.Run("unknown destination", func(t *testing.T) {
		cat, _, _ := newMoveTestCatalog(t)
		var notFound *PoolNotFoundError
		if _, _, err := cat.moveDropsEncryption(ctx, "acme", "ghost"); !errors.As(err, &notFound) || notFound.Pool != "ghost" {
			t.Fatalf("moveDropsEncryption = %v, want *PoolNotFoundError for ghost", err)
		}
	})

	t.Run("unknown source", func(t *testing.T) {
		cat, kv, _ := newMoveTestCatalog(t)
		rewriteRecord(t, kv, AssignmentKey("acme"), func(a *Assignment) { a.Pool = "vanished" })
		var notFound *PoolNotFoundError
		if _, _, err := cat.moveDropsEncryption(ctx, "acme", "bulk"); !errors.As(err, &notFound) || notFound.Pool != "vanished" {
			t.Fatalf("moveDropsEncryption = %v, want *PoolNotFoundError for vanished", err)
		}
	})

	t.Run("plain to plain keeps nothing to drop", func(t *testing.T) {
		cat, _, _ := newMoveTestCatalog(t)
		from, drops, err := cat.moveDropsEncryption(ctx, "acme", "bulk")
		if err != nil || from != "fast" || drops {
			t.Fatalf("moveDropsEncryption = (%q, %v, %v), want (fast, false, nil)", from, drops, err)
		}
	})
}

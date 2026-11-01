package engine

import (
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// newExclusiveApplyEngine returns an MVCCEngine over memory storage with lr
// as its lessor (none when lr is nil).
func newExclusiveApplyEngine(t *testing.T, lr *lease.Lessor) *MVCCEngine {
	t.Helper()
	stg := memory.NewStorage()
	t.Cleanup(func() { _ = stg.Close() })
	var opts []MVCCOption
	if lr != nil {
		opts = append(opts, WithMVCCLessor(lr))
		t.Cleanup(lr.Close)
	}
	eng, err := NewMVCCEngine(stg, opts...)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	return eng
}

func exclusiveApplyEntry(t *testing.T, cmd []byte, err error) sm.Entry {
	t.Helper()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return sm.Entry{Index: 1, Cmd: cmd}
}

func TestRequiresExclusiveApply_PlainWritesShareTheLock(t *testing.T) {
	for name, lr := range map[string]*lease.Lessor{"no lessor": nil, "empty lessor": lease.NewLessor()} {
		eng := newExclusiveApplyEngine(t, lr)
		put, perr := protocol.EncodeMVCCPut([]byte("a"), []byte("1"), 0)
		del, derr := protocol.EncodeMVCCDelete([]byte("a"))
		entries := []sm.Entry{exclusiveApplyEntry(t, put, perr), exclusiveApplyEntry(t, del, derr)}
		if eng.RequiresExclusiveApply(entries) {
			t.Errorf("%s: a plain KV batch takes the exclusive path", name)
		}
		if eng.RequiresExclusiveApply(nil) {
			t.Errorf("%s: an empty batch takes the exclusive path", name)
		}
	}
}

func TestRequiresExclusiveApply_SideStateBatchesAreExclusive(t *testing.T) {
	eng := newExclusiveApplyEngine(t, nil)
	put, perr := protocol.EncodeMVCCPut([]byte("a"), []byte("1"), 0)
	plain := exclusiveApplyEntry(t, put, perr)
	idxCreate, cerr := protocol.EncodeIndexCreateOpts("idx", "name", protocol.IndexCreateOptions{})
	idxDrop, derr := protocol.EncodeIndexDrop("idx")
	grant, gerr := protocol.EncodeLeaseGrant(60)
	revoke, rerr := protocol.EncodeLeaseRevoke(7)
	auditAppend, aerr := protocol.EncodeAuditAppend("acme", tenantEvent("acme", "barrier", "unseal"))
	auditPrune, perr := protocol.EncodeAuditPrune("acme", 1)
	cases := map[string]sm.Entry{
		"index create": exclusiveApplyEntry(t, idxCreate, cerr),
		"index drop":   exclusiveApplyEntry(t, idxDrop, derr),
		"lease grant":  exclusiveApplyEntry(t, grant, gerr),
		"lease revoke": exclusiveApplyEntry(t, revoke, rerr),
		"audit append": exclusiveApplyEntry(t, auditAppend, aerr),
		"audit prune":  exclusiveApplyEntry(t, auditPrune, perr),
		"undecodable":  {Index: 1},
	}
	for name, e := range cases {
		if !eng.RequiresExclusiveApply([]sm.Entry{plain, e}) {
			t.Errorf("%s: the batch keeps the shared path", name)
		}
	}
}

// guardedExclusiveApplyEntry wraps inner in an OpGuarded "fence is absent".
func guardedExclusiveApplyEntry(t *testing.T, inner []byte, err error) sm.Entry {
	t.Helper()
	if err != nil {
		t.Fatalf("encode inner: %v", err)
	}
	c, cerr := protocol.EncodeCompare([]byte("fence"), protocol.CompareTargetVersion, protocol.CompareResultEqual, nil, 0, 0, 0)
	cmd, gerr := protocol.EncodeGuarded([][]byte{c}, inner)
	if cerr != nil || gerr != nil {
		t.Fatalf("encode guard: %v %v", cerr, gerr)
	}
	return sm.Entry{Index: 1, Cmd: cmd}
}

// handleGuarded dispatches its inner command straight through
// updateHandlers, so a guarded side-state command builds its batch exactly as
// the bare one does and must be classified by its inner opcode.
func TestRequiresExclusiveApply_GuardedSideStateCommandsAreExclusive(t *testing.T) {
	eng := newExclusiveApplyEngine(t, lease.NewLessor())
	idxCreate, cerr := protocol.EncodeIndexCreateOpts("idx", "name", protocol.IndexCreateOptions{})
	idxDrop, derr := protocol.EncodeIndexDrop("idx")
	grant, gerr := protocol.EncodeLeaseGrant(60)
	auditAppend, aerr := protocol.EncodeAuditAppend("acme", tenantEvent("acme", "barrier", "unseal"))
	compactPrefix, perr := protocol.EncodeMVCCCompactPrefix([]byte("p/"), 1)
	compactNode, nerr := protocol.EncodeMVCCCompactNode(1)
	cases := map[string]sm.Entry{
		"guarded index create":   guardedExclusiveApplyEntry(t, idxCreate, cerr),
		"guarded index drop":     guardedExclusiveApplyEntry(t, idxDrop, derr),
		"guarded lease grant":    guardedExclusiveApplyEntry(t, grant, gerr),
		"guarded audit append":   guardedExclusiveApplyEntry(t, auditAppend, aerr),
		"guarded compact prefix": guardedExclusiveApplyEntry(t, compactPrefix, perr),
		"guarded compact node":   guardedExclusiveApplyEntry(t, compactNode, nerr),
		"undecodable guard":      {Index: 1, Cmd: []byte{protocol.OpGuarded, 1}},
	}
	for name, e := range cases {
		if !eng.RequiresExclusiveApply([]sm.Entry{e}) {
			t.Errorf("%s: the batch keeps the shared path", name)
		}
	}
}

// A guarded plain write touches no side state, so with no lease it keeps the
// shared path like the bare write.
func TestRequiresExclusiveApply_GuardedPlainWritesShareTheLock(t *testing.T) {
	eng := newExclusiveApplyEngine(t, lease.NewLessor())
	put, perr := protocol.EncodeMVCCPut([]byte("a"), []byte("1"), 0)
	del, derr := protocol.EncodeMVCCDelete([]byte("a"))
	entries := []sm.Entry{guardedExclusiveApplyEntry(t, put, perr), guardedExclusiveApplyEntry(t, del, derr)}
	if eng.RequiresExclusiveApply(entries) {
		t.Error("a guarded plain write takes the exclusive path")
	}
}

func TestRequiresExclusiveApply_AnyWriteOnceALeaseExists(t *testing.T) {
	lr := lease.NewLessor()
	eng := newExclusiveApplyEngine(t, lr)
	batch := memory.NewStorage().Batch()
	if _, err := lr.Grant(batch, 60); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	put, perr := protocol.EncodeMVCCPut([]byte("a"), []byte("1"), 0)
	if !eng.RequiresExclusiveApply([]sm.Entry{exclusiveApplyEntry(t, put, perr)}) {
		t.Error("a write with a lease in the lessor keeps the shared path")
	}
}

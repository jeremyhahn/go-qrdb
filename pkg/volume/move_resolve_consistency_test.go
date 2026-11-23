package volume

import (
	"context"
	"errors"
	"testing"
)

// journalReadHookKV runs beforeJournalRead (one-shot unless repeat) right
// before a Get of the tenant's move journal: a writer committing between
// ResolveForNode's assignment read and its journal read.
type journalReadHookKV struct {
	KVServicer
	tenantID          string
	repeat            bool
	beforeJournalRead func()
}

func (k *journalReadHookKV) Get(ctx context.Context, key string) ([]byte, error) {
	if key == MoveKey(k.tenantID) && k.beforeJournalRead != nil {
		hook := k.beforeJournalRead
		if !k.repeat {
			k.beforeJournalRead = nil
		}
		hook()
	}
	return k.KVServicer.Get(ctx, key)
}

func hookJournalRead(cat *Catalog, repeat bool, hook func()) {
	cat.kv = &journalReadHookKV{KVServicer: cat.kv, tenantID: "acme", repeat: repeat, beforeJournalRead: hook}
}

func TestCatalog_ResolveForNode_CutOverBetweenReadsNamesTheDestination(t *testing.T) {
	cat, _, _ := copiedMove(t)
	ctx := context.Background()
	hookJournalRead(cat, false, func() {
		if _, err := cat.CutOverMove(ctx, "acme"); err != nil {
			t.Errorf("CutOverMove: %v", err)
		}
	})
	a, err := cat.ResolveForNode(ctx, "acme", "n1")
	if err != nil || a.Pool != "bulk" {
		t.Fatalf("ResolveForNode across a cut-over = %+v, %v; want bulk", a, err)
	}
}

func TestCatalog_ResolveForNode_RetireBetweenReadsNamesTheDestination(t *testing.T) {
	cat, kv, _ := copiedMove(t)
	ctx := context.Background()
	hookJournalRead(cat, false, func() {
		if _, err := cat.CutOverMove(ctx, "acme"); err != nil {
			t.Errorf("CutOverMove: %v", err)
			return
		}
		// The retire deletes the journal.
		raw, err := kv.Get(ctx, MoveKey("acme"))
		if err != nil || raw == nil {
			t.Errorf("journal after cut-over = %q, %v", raw, err)
			return
		}
		if ok, err := kv.CompareAndDelete(ctx, MoveKey("acme"), raw); err != nil || !ok {
			t.Errorf("delete journal = %v, %v", ok, err)
		}
	})
	a, err := cat.ResolveForNode(ctx, "acme", "n1")
	if err != nil || a.Pool != "bulk" {
		t.Fatalf("ResolveForNode across a cut-over and retire = %+v, %v; want bulk", a, err)
	}
	if raw, _ := kv.Get(ctx, MoveKey("acme")); raw != nil {
		t.Fatal("journal still present; the retire case did not run")
	}
}

func TestCatalog_ResolveForNode_BoundedRetryOnUnsettledAssignment(t *testing.T) {
	cat, kv, _ := copiedMove(t)
	ctx := context.Background()
	hooks := 0
	hookJournalRead(cat, true, func() {
		hooks++
		raw, err := kv.Get(ctx, AssignmentKey("acme"))
		if err != nil || raw == nil {
			t.Errorf("assignment = %q, %v", raw, err)
			return
		}
		// Same assignment, different record bytes: a rewrite on every pass.
		if err := kv.Put(ctx, AssignmentKey("acme"), append(raw, ' ')); err != nil {
			t.Errorf("Put: %v", err)
		}
	})
	_, err := cat.ResolveForNode(ctx, "acme", "n1")
	var ie *ResolveInconsistentError
	if !errors.As(err, &ie) || ie.TenantID != "acme" || ie.Attempts != resolveForNodeAttempts {
		t.Fatalf("ResolveForNode = %v, want *ResolveInconsistentError after %d attempts", err, resolveForNodeAttempts)
	}
	if hooks != resolveForNodeAttempts {
		t.Fatalf("journal reads = %d, want %d", hooks, resolveForNodeAttempts)
	}
}

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
)

type countingReconciler struct {
	calls int
	err   error
	ctxOK bool
}

func (c *countingReconciler) ReconcileTenantHomes(ctx context.Context) error {
	c.calls++
	_, c.ctxOK = ctx.Deadline()
	return c.err
}

// TestTenantHomeHook_ReconcilesOnJoinAndLeave checks a join and a leave
// each run one bounded reconcile (a join grows the homes, a leave sheds the
// departed node), and a reconcile error does not panic.
func TestTenantHomeHook_ReconcilesOnJoinAndLeave(t *testing.T) {
	r := &countingReconciler{err: errors.New("boom")}
	hook := tenantHomeHook(r)
	m := cluster.Member{NodeID: 4}

	hook(context.Background(), m, true)
	if r.calls != 1 {
		t.Fatalf("join ran %d reconciles, want 1", r.calls)
	}
	if !r.ctxOK {
		t.Fatal("reconcile context has no deadline")
	}
	hook(context.Background(), m, false)
	if r.calls != 2 {
		t.Fatalf("join then leave ran %d reconciles, want 2", r.calls)
	}
	tenantHomeHook(nil)(context.Background(), m, true) // no reconciler: no-op
}

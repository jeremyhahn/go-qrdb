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

// TestTenantHomeHook_ReconcilesOnJoinOnly checks a join runs one bounded
// reconcile, a leave none, and a reconcile error does not panic.
func TestTenantHomeHook_ReconcilesOnJoinOnly(t *testing.T) {
	r := &countingReconciler{err: errors.New("boom")}
	hook := tenantHomeHook(r)
	m := cluster.Member{NodeID: 4}

	hook(context.Background(), m, false)
	if r.calls != 0 {
		t.Fatalf("leave ran %d reconciles, want 0", r.calls)
	}
	hook(context.Background(), m, true)
	if r.calls != 1 {
		t.Fatalf("join ran %d reconciles, want 1", r.calls)
	}
	if !r.ctxOK {
		t.Fatal("reconcile context has no deadline")
	}
	tenantHomeHook(nil)(context.Background(), m, true) // no reconciler: no-op
}

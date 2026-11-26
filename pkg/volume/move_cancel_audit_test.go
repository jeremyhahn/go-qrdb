package volume

import (
	"context"
	"errors"
	"testing"
)

type cancelCtxKey struct{}

// cancelRecord is one RecordMoveTransition call with the value the test put
// on the caller's context, so a record proves the request ctx reached it.
type cancelRecord struct {
	recordedTransition
	ctxValue any
}

// cancelRecorder is a MoveTransitionRecorder double that keeps every call
// with its ctx's test value and returns err.
type cancelRecorder struct {
	calls []cancelRecord
	err   error
}

func (r *cancelRecorder) RecordMoveTransition(ctx context.Context, transition, tenantID, nodeID, phase string) error {
	r.calls = append(r.calls, cancelRecord{
		recordedTransition: recordedTransition{transition: transition, tenant: tenantID, node: nodeID, phase: phase},
		ctxValue:           ctx.Value(cancelCtxKey{}),
	})
	return r.err
}

// An operator's cancel is recorded once, with the request's ctx, when it
// changes the journal; a repeat that changes nothing records nothing; a
// refused cancel records the refusal; with no recorder nothing is called.
func TestCatalog_CancelMove_RecordsTheOperatorsCancel(t *testing.T) {
	ctx := context.WithValue(context.Background(), cancelCtxKey{}, "operator-request")
	setups := map[string]func(t *testing.T) (*Catalog, *fakeKV, *fakeTx){
		"admitted": newAdmittedMove,
		"copied":   copiedMove,
		"running copy": func(t *testing.T) (*Catalog, *fakeKV, *fakeTx) {
			cat, kv, tx := newAdmittedMove(t)
			if _, err := cat.GrantMoveStep(ctx, "acme"); err != nil {
				t.Fatal(err)
			}
			if _, err := cat.CompleteMoveStep(ctx, "acme", "n1"); err != nil {
				t.Fatal(err)
			}
			if _, err := cat.GrantMoveStep(ctx, "acme"); err != nil {
				t.Fatal(err)
			}
			return cat, kv, tx
		},
	}
	for name, setup := range setups {
		t.Run(name, func(t *testing.T) {
			cat, _, _ := setup(t)
			rec := &cancelRecorder{}
			cat.SetMoveTransitionRecorder(rec)
			if _, err := cat.CancelMove(ctx, "acme"); err != nil {
				t.Fatalf("CancelMove: %v", err)
			}
			want := cancelRecord{recordedTransition: recordedTransition{transition: MoveTransitionCancel, tenant: "acme"}, ctxValue: "operator-request"}
			if len(rec.calls) != 1 || rec.calls[0] != want {
				t.Fatalf("records = %+v, want one %+v", rec.calls, want)
			}
			if _, err := cat.CancelMove(ctx, "acme"); err != nil {
				t.Fatalf("second CancelMove: %v", err)
			}
			if len(rec.calls) != 1 {
				t.Fatalf("the idempotent repeat recorded: %+v", rec.calls)
			}
		})
	}
	t.Run("refused past commit", func(t *testing.T) {
		cat, _, _ := cutOverMove(t)
		rec := &cancelRecorder{}
		cat.SetMoveTransitionRecorder(rec)
		_, err := cat.CancelMove(ctx, "acme")
		wantConflict(t, "CancelMove on cutover", err)
		want := cancelRecord{recordedTransition: recordedTransition{transition: MoveTransitionCancelRefused, tenant: "acme"}, ctxValue: "operator-request"}
		if len(rec.calls) != 1 || rec.calls[0] != want {
			t.Fatalf("records = %+v, want one %+v", rec.calls, want)
		}
	})
	t.Run("store failure records nothing", func(t *testing.T) {
		cat, _, tx := newAdmittedMove(t)
		rec := &cancelRecorder{}
		cat.SetMoveTransitionRecorder(rec)
		boom := errors.New("execute failed")
		tx.failExecute = boom
		if _, err := cat.CancelMove(ctx, "acme"); !errors.Is(err, boom) {
			t.Fatalf("CancelMove error = %v, want the execute failure", err)
		}
		if len(rec.calls) != 0 {
			t.Fatalf("a failed write recorded: %+v", rec.calls)
		}
	})
	t.Run("recorder error keeps the cancel", func(t *testing.T) {
		cat, _, _ := newAdmittedMove(t)
		rec := &cancelRecorder{err: errors.New("audit sink down")}
		cat.SetMoveTransitionRecorder(rec)
		m, err := cat.CancelMove(ctx, "acme")
		if err != nil || m.State != MoveStateAborting {
			t.Fatalf("CancelMove = %+v, %v, want aborting", m, err)
		}
		if len(rec.calls) != 1 {
			t.Fatalf("records = %+v, want one", rec.calls)
		}
	})
	t.Run("cleared recorder", func(t *testing.T) {
		cat, _, _ := newAdmittedMove(t)
		rec := &cancelRecorder{}
		cat.SetMoveTransitionRecorder(rec)
		cat.SetMoveTransitionRecorder(nil)
		if _, err := cat.CancelMove(ctx, "acme"); err != nil {
			t.Fatalf("CancelMove: %v", err)
		}
		if len(rec.calls) != 0 {
			t.Fatalf("a cleared recorder was called: %+v", rec.calls)
		}
	})
}

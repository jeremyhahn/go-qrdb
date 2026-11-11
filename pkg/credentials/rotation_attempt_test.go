package credentials

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// A credential whose value source does not return is given up after one
// attempt's bound, well inside its claim's lease, and the pass goes on: the
// credential sorted after it rotates in the same RotateDue call, and again
// in the next one.
func TestRotateDue_SlowSourceDoesNotStarveTheNextCredential(t *testing.T) {
	// The bound of one attempt.
	const attempt = 30 * time.Second
	synctest.Test(t, func(t *testing.T) {
		svc, _ := newFaultRotationService(t)
		setFaultPolicy(t, svc, "a", 5)
		setFaultPolicy(t, svc, "b", 5)
		source := svc.rotationSource
		var stalled atomic.Int32
		svc.rotationSource = func(ctx context.Context, name string) (*secure.Buffer, error) {
			if name != "a" {
				return source(ctx, name)
			}
			stalled.Add(1)
			<-ctx.Done()
			return nil, ctx.Err()
		}

		due := rotationEpoch.Add(time.Hour)
		for pass, want := range []string{"b-rotated-1", "b-rotated-2"} {
			start := time.Now()
			n, err := svc.RotateDue(t.Context(), due.Add(time.Duration(pass)*time.Hour))
			if n != 1 {
				t.Fatalf("pass %d: RotateDue rotated %d, want 1: b is due behind the stalled a", pass, n)
			}
			var rotErr *RotationError
			if !errors.As(err, &rotErr) || rotErr.Name != "a" || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("pass %d: RotateDue error = %v; want a's attempt past its deadline", pass, err)
			}
			if spent := time.Since(start); spent > attempt {
				t.Fatalf("pass %d: the stalled attempt held the pass for %v, want at most %v", pass, spent, attempt)
			}
			if got := stalled.Load(); got != int32(pass+1) {
				t.Fatalf("pass %d: the stalled source ran %d times, want %d", pass, got, pass+1)
			}
			got, err := svc.store.Get(t.Context(), credentialPrefix+"b")
			if err != nil || string(got) != want {
				t.Fatalf("pass %d: b = %q, %v; want %q", pass, got, err, want)
			}
		}
	})
}

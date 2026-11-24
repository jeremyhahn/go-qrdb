//go:build clusterproof

package soak

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/workload"
)

// stallReplicas is a cluster whose replica states never answer: the call
// returns only when its context ends, as a hung node's would.
type stallReplicas struct{}

func (stallReplicas) ReplicaStates(ctx context.Context) ([]ReplicaState, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestRun_PostPhaseDeadline is #1637's stalled soak: after the duration a
// post-run check hangs. One deadline bounds the whole post-duration phase,
// so the verdict and the bundle are still written, the phase that ran over
// is named on the progress writer, and each fault event is printed as it
// happens.
func TestRun_PostPhaseDeadline(t *testing.T) {
	t.Run("replicas stall", func(t *testing.T) {
		kv := newFakeKV()
		o := testOptions(t, kv, &fakeNemesis{})
		o.Replicas = stallReplicas{}
		o.RecoveryTimeout = time.Minute
		o.PostTimeout = 300 * time.Millisecond
		var progress bytes.Buffer
		o.Progress = &progress
		o.ProgressInterval = time.Hour

		start := time.Now()
		res, err := Run(context.Background(), o)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if took := time.Since(start); took > 20*time.Second {
			t.Fatalf("Run took %v: the post-run deadline did not bound the stalled check", took)
		}
		if res.ManifestSHA256 == "" {
			t.Fatal("no bundle manifest: the run ended without a bundle")
		}
		out := progress.String()
		for _, want := range []string{": healing\n", ": liveness\n", ": replicas\n", ": writing bundle\n", "window 0 fake", "[n1]: ok"} {
			if !strings.Contains(out, want) {
				t.Errorf("progress lacks %q:\n%s", want, out)
			}
		}
		if strings.Index(out, ": replicas\n") > strings.Index(out, ": writing bundle\n") {
			t.Errorf("phases out of order:\n%s", out)
		}
	})

	// #1637 fx1637e: the final read's operations are bounded by the
	// post-run deadline, not only by OpTimeout. A read that stalls until its
	// context ends, with OpTimeout far past PostTimeout, must not hold the run.
	t.Run("final read stalls", func(t *testing.T) {
		kv := &stallGetKV{kv: newFakeKV()}
		o := testOptions(t, kv.kv, &fakeNemesis{})
		o.Dial = kv.dial
		o.Workload.OpTimeout = workload.Duration(10 * time.Minute)
		o.RecoveryTimeout = time.Minute
		o.PostTimeout = 300 * time.Millisecond
		var progress bytes.Buffer
		o.Progress = &stallOnPhase{w: &progress, phase: ": final read\n", stall: &kv.stall}
		o.ProgressInterval = time.Hour

		start := time.Now()
		res, err := Run(context.Background(), o)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if took := time.Since(start); took > 20*time.Second {
			t.Fatalf("Run took %v: the post-run deadline did not bound the stalled final read", took)
		}
		if !kv.stall.Load() {
			t.Fatalf("the final read phase never began:\n%s", progress.String())
		}
		if res.ManifestSHA256 == "" {
			t.Fatal("no bundle manifest: the run ended without a bundle")
		}
		found := false
		for _, r := range res.Verdict.Reasons {
			if strings.HasPrefix(r, "post-run phase: not finished") {
				found = true
			}
		}
		if !found {
			t.Errorf("verdict reasons lack the post-run deadline: %q", res.Verdict.Reasons)
		}
		if !strings.Contains(progress.String(), ": writing bundle\n") {
			t.Errorf("progress lacks the bundle phase:\n%s", progress.String())
		}
	})
}

// stallGetKV serves the fake store, except that once stall is set every Get
// answers only when its context ends, as a half-open session's would.
type stallGetKV struct {
	kv    *fakeKV
	stall atomic.Bool
}

func (s *stallGetKV) dial(ctx context.Context, addr string) (workload.Conn, error) {
	c, err := s.kv.dial(ctx, addr)
	if err != nil {
		return nil, err
	}
	return stallGetConn{Conn: c, stall: &s.stall}, nil
}

type stallGetConn struct {
	workload.Conn
	stall *atomic.Bool
}

func (c stallGetConn) Get(ctx context.Context, key string, opts ...transport.ReadOption) ([]byte, error) {
	if c.stall.Load() {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return c.Conn.Get(ctx, key, opts...)
}

// stallOnPhase sets stall when the run prints phase, so only the operations
// of that phase and after it stall.
type stallOnPhase struct {
	w     *bytes.Buffer
	phase string
	stall *atomic.Bool
}

func (p *stallOnPhase) Write(b []byte) (int, error) {
	if strings.Contains(string(b), p.phase) {
		p.stall.Store(true)
	}
	return p.w.Write(b)
}

// TestPostTimeout_Default derives the deadline from one heal per fault and
// one recovery wait per cluster check when PostTimeout is zero.
func TestPostTimeout_Default(t *testing.T) {
	o := Options{HealTimeout: time.Minute, RecoveryTimeout: 2 * time.Minute}
	o.Nemeses = testOptions(t, newFakeKV(), &fakeNemesis{}).Nemeses
	if got, want := o.postTimeout(), time.Minute+6*time.Minute; got != want {
		t.Fatalf("postTimeout = %v, want %v", got, want)
	}
	o.PostTimeout = time.Second
	if got := o.postTimeout(); got != time.Second {
		t.Fatalf("postTimeout = %v, want the configured 1s", got)
	}
}

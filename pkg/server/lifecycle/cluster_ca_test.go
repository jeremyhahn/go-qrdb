// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package lifecycle

import (
	"context"
	"crypto"
	"crypto/x509"
	"errors"
	"strings"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// flakyClusterCAProvider is a minimal cakey.Provider double (it reuses
// stubClusterCAProvider's shape via embedding is unnecessary -- this one
// additionally simulates a transient *service.NotLeaderError for a
// configured number of calls before succeeding, and an optional permanent
// failure) for exercising AwaitClusterCACertificate's retry path and
// EstablishClusterCA's wrapping of a permanent one.
type flakyClusterCAProvider struct {
	cert           *x509.Certificate
	notLeaderCalls int   // answers *service.NotLeaderError this many times first
	transientErr   error // when set, answered instead of NotLeaderError for those calls
	permanentErr   error // when set, answered immediately, never retried
	calls          int
}

func (f *flakyClusterCAProvider) Signer(context.Context) (crypto.Signer, error) { return nil, nil }

func (f *flakyClusterCAProvider) Certificate(context.Context) (*x509.Certificate, error) {
	f.calls++
	if f.permanentErr != nil {
		return nil, f.permanentErr
	}
	if f.calls <= f.notLeaderCalls {
		if f.transientErr != nil {
			return nil, f.transientErr
		}
		return nil, &service.NotLeaderError{ShardID: 1}
	}
	return f.cert, nil
}

// TestAwaitClusterCACertificate_RetriesTransientNotLeader proves the
// exported retry loop survives a transient *service.NotLeaderError and
// succeeds once the provider does, confirming the logic
// cmd/qrdbd's awaitClusterCACertificate now delegates to behaves
// identically from its new, shared home (go-qrdb#591's review, B5).
func TestAwaitClusterCACertificate_RetriesTransientNotLeader(t *testing.T) {
	want := &x509.Certificate{SerialNumber: nil}
	stub := &flakyClusterCAProvider{cert: want, notLeaderCalls: 2}

	got, err := AwaitClusterCACertificate(context.Background(), stub, time.Second, time.Millisecond, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("AwaitClusterCACertificate: %v", err)
	}
	if got != want {
		t.Error("expected the certificate returned once the transient NotLeaderError stopped")
	}
	if stub.calls != 3 {
		t.Errorf("Certificate called %d times, want 3 (2 transient failures + 1 success)", stub.calls)
	}
}

// TestAwaitClusterCACertificate_RetriesReadIndexNotReady proves a freshly
// elected leader's ReadIndex warm-up is waited out, not treated as fatal. The
// error is wrapped exactly as the built-in provider returns it on a restarted
// node right after its unseal -- cakey.StoreError over service.ReadError over
// quicraft.ErrReadIndexNotReady -- which used to make qrdbd exit at boot.
func TestAwaitClusterCACertificate_RetriesReadIndexNotReady(t *testing.T) {
	want := &x509.Certificate{}
	stub := &flakyClusterCAProvider{cert: want, notLeaderCalls: 2, transientErr: &cakey.StoreError{
		Op: "get", Detail: "__sys:cluster_ca/certificate", Err: &service.ReadError{Err: quicraft.ErrReadIndexNotReady},
	}}

	got, err := AwaitClusterCACertificate(context.Background(), stub, time.Second, time.Millisecond, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("AwaitClusterCACertificate: %v", err)
	}
	if got != want {
		t.Error("expected the certificate returned once the read index became ready")
	}
	if stub.calls != 3 {
		t.Errorf("Certificate called %d times, want 3 (2 not-ready reads + 1 success)", stub.calls)
	}
}

// TestAwaitClusterCACertificate_ReadIndexNotReadyPastDeadline proves a read
// index that never becomes ready still fails the boot once the deadline
// passes, returning the not-ready error itself.
func TestAwaitClusterCACertificate_ReadIndexNotReadyPastDeadline(t *testing.T) {
	stub := &flakyClusterCAProvider{notLeaderCalls: 1 << 30, transientErr: &service.ReadError{Err: quicraft.ErrReadIndexNotReady}}
	const deadline = 20 * time.Millisecond

	start := time.Now()
	_, err := AwaitClusterCACertificate(context.Background(), stub, deadline, time.Millisecond, 5*time.Millisecond)
	elapsed := time.Since(start)
	if !errors.Is(err, quicraft.ErrReadIndexNotReady) {
		t.Fatalf("AwaitClusterCACertificate = %v, want quicraft.ErrReadIndexNotReady", err)
	}
	var sre *server.StartupReadError
	if !errors.As(err, &sre) {
		t.Fatalf("AwaitClusterCACertificate = %v (%T), want *server.StartupReadError", err, err)
	}
	if sre.Attempts != stub.calls || stub.calls < 1 {
		t.Fatalf("StartupReadError.Attempts = %d, Certificate called %d times, want every attempt counted", sre.Attempts, stub.calls)
	}
	// How many attempts fit in the deadline is the host's to decide: a runner
	// that holds the first one past it leaves room for no other (run 56220,
	// one attempt in 54 ms of a 20 ms deadline). What the read promises is
	// that it waits a transient out until the deadline, so it never gives
	// up before it; a read that stopped retrying would return at once.
	if elapsed < deadline {
		t.Fatalf("AwaitClusterCACertificate gave up after %v and %d attempts, want it to retry until its %v deadline", elapsed, stub.calls, deadline)
	}
}

// hungClusterCAProvider answers Certificate only when the caller's context
// ends, as a ReadIndex handed to a leader that died does.
type hungClusterCAProvider struct {
	flakyClusterCAProvider
}

func (h *hungClusterCAProvider) Certificate(ctx context.Context) (*x509.Certificate, error) {
	h.calls++
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestAwaitClusterCACertificate_HungReadFailsTheBoot(t *testing.T) {
	stub := &hungClusterCAProvider{}

	start := time.Now()
	_, err := AwaitClusterCACertificate(context.Background(), stub, 100*time.Millisecond, time.Millisecond, 5*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("AwaitClusterCACertificate took %s on a read that never returns, want the 100ms deadline honoured", elapsed)
	}
	var sre *server.StartupReadError
	if !errors.As(err, &sre) {
		t.Fatalf("AwaitClusterCACertificate = %v (%T), want *server.StartupReadError", err, err)
	}
	if sre.TimedOut < 1 || stub.calls != sre.Attempts {
		t.Errorf("calls=%d Attempts=%d TimedOut=%d, want every attempt counted and the hung ones cut off", stub.calls, sre.Attempts, sre.TimedOut)
	}
}

// TestAwaitClusterCACertificate_PermanentErrorReturnsImmediately proves a
// non-NotLeaderError is never retried.
func TestAwaitClusterCACertificate_PermanentErrorReturnsImmediately(t *testing.T) {
	boom := errors.New("sealed")
	stub := &flakyClusterCAProvider{permanentErr: boom}

	start := time.Now()
	_, err := AwaitClusterCACertificate(context.Background(), stub, time.Minute, 200*time.Millisecond, time.Second)
	elapsed := time.Since(start)
	if !errors.Is(err, boom) {
		t.Fatalf("AwaitClusterCACertificate = %v, want %v", err, boom)
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("AwaitClusterCACertificate took %s, want an immediate return with no backoff sleep", elapsed)
	}
	if stub.calls != 1 {
		t.Errorf("Certificate called %d times, want exactly 1", stub.calls)
	}
}

// TestEstablishClusterCA_NilProviderIsNoOp proves EstablishClusterCA never
// errors for a composer with no cluster-CA concept at all (BYO-TLS,
// Infra.NoClusterCAKey) -- Node.ClusterCAProv stays nil in that
// configuration, and this must not be mistaken for a failure.
func TestEstablishClusterCA_NilProviderIsNoOp(t *testing.T) {
	if err := EstablishClusterCA(context.Background(), &Node{}); err != nil {
		t.Fatalf("EstablishClusterCA with a nil ClusterCAProv = %v, want nil", err)
	}
	if err := EstablishClusterCA(context.Background(), nil); err != nil {
		t.Fatalf("EstablishClusterCA(nil Node) = %v, want nil", err)
	}
}

// TestEstablishClusterCA_Succeeds proves a normal, already-establishable
// provider resolves without error.
func TestEstablishClusterCA_Succeeds(t *testing.T) {
	stub := &flakyClusterCAProvider{cert: &x509.Certificate{}}
	n := &Node{ClusterCAProv: stub}
	if err := EstablishClusterCA(context.Background(), n); err != nil {
		t.Fatalf("EstablishClusterCA: %v", err)
	}
	if stub.calls != 1 {
		t.Errorf("Certificate called %d times, want 1", stub.calls)
	}
}

// TestEstablishClusterCA_WrapsFailure proves a permanent failure is
// reported as *ClusterCAEstablishError, naming the underlying cause.
func TestEstablishClusterCA_WrapsFailure(t *testing.T) {
	boom := errors.New("barrier sealed")
	stub := &flakyClusterCAProvider{permanentErr: boom}
	n := &Node{ClusterCAProv: stub}

	err := EstablishClusterCA(context.Background(), n)
	var estErr *ClusterCAEstablishError
	if !errors.As(err, &estErr) {
		t.Fatalf("EstablishClusterCA = %v (%T), want *ClusterCAEstablishError", err, err)
	}
	if !errors.Is(err, boom) {
		t.Errorf("EstablishClusterCA error does not unwrap to the underlying cause: %v", err)
	}
	if !strings.Contains(estErr.Error(), boom.Error()) {
		t.Errorf("Error() = %q, want it to contain %q", estErr.Error(), boom.Error())
	}
}

// TestAwaitClusterCACertificate_DeadlineExceeded proves a deadline that has
// already elapsed by the time a transient *service.NotLeaderError is
// observed gives up immediately, reporting that error, rather than
// sleeping through at least one more backoff first.
func TestAwaitClusterCACertificate_DeadlineExceeded(t *testing.T) {
	stub := &flakyClusterCAProvider{notLeaderCalls: 1000000}

	_, err := AwaitClusterCACertificate(context.Background(), stub, 0, time.Second, 5*time.Second)
	var notLeader *service.NotLeaderError
	if !errors.As(err, &notLeader) {
		t.Fatalf("AwaitClusterCACertificate = %v (%T), want *service.NotLeaderError", err, err)
	}
	if stub.calls != 1 {
		t.Errorf("Certificate called %d times, want exactly 1 (deadline already elapsed)", stub.calls)
	}
}

// TestAwaitClusterCACertificate_CtxCanceledDuringBackoff proves a ctx
// cancellation during the backoff wait is honored immediately rather than
// waiting out the (here, long) remaining delay.
func TestAwaitClusterCACertificate_CtxCanceledDuringBackoff(t *testing.T) {
	stub := &flakyClusterCAProvider{notLeaderCalls: 1000000}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := AwaitClusterCACertificate(ctx, stub, time.Minute, time.Second, 5*time.Second)
	elapsed := time.Since(start)
	var notLeader *service.NotLeaderError
	if !errors.As(err, &notLeader) {
		t.Fatalf("AwaitClusterCACertificate = %v (%T), want *service.NotLeaderError", err, err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("AwaitClusterCACertificate took %s, want an immediate return once ctx was canceled", elapsed)
	}
}

// TestClusterCAEstablishError_Error proves the message names both the
// fixed prefix and the underlying cause, rather than only being reachable
// through errors.As/errors.Is.
func TestClusterCAEstablishError_Error(t *testing.T) {
	err := &ClusterCAEstablishError{Err: errors.New("barrier sealed")}
	msg := err.Error()
	if !strings.Contains(msg, "cluster CA establishment failed") {
		t.Errorf("Error() = %q, missing the fixed prefix", msg)
	}
	if !strings.Contains(msg, "barrier sealed") {
		t.Errorf("Error() = %q, missing the underlying cause", msg)
	}
}

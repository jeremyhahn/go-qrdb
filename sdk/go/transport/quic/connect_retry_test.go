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

package quic

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// refusedCode is the application error code refusingServer closes a refused
// connection with.
const refusedCode quicgo.ApplicationErrorCode = 0x51

// refusingServer is a QUIC listener that closes the first refuse connections
// it accepts before reading anything from them -- the shape of a server that
// sheds a connection it cannot serve yet, as pkg/api/connlimit sheds an
// over-cap peer -- and answers admin.Health on every later one.
type refusingServer struct {
	ln     *quicgo.Listener
	refuse int64

	// accepted counts the connections the server completed a handshake on.
	accepted atomic.Int64
	// refused receives one value per refused connection, after the close.
	refused chan struct{}
}

// startRefusingServer starts a refusingServer on a random local port and
// returns it with the client TLS config that trusts it.
func startRefusingServer(t *testing.T, refuse int64) (*refusingServer, *tls.Config) {
	t.Helper()

	serverCfg, clientCfg := e2eTLS(t)
	ln, err := quicgo.ListenAddr("127.0.0.1:0", serverCfg, &quicgo.Config{MaxIdleTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	s := &refusingServer{ln: ln, refuse: refuse, refused: make(chan struct{}, 64)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.serve()
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
	})
	return s, clientCfg
}

func (s *refusingServer) serve() {
	for {
		conn, err := s.ln.Accept(context.Background())
		if err != nil {
			return
		}
		if s.accepted.Add(1) <= s.refuse {
			_ = conn.CloseWithError(refusedCode, "refused")
			select {
			case s.refused <- struct{}{}:
			default:
			}
			continue
		}
		go serveHealth(conn)
	}
}

// serveHealth answers every request on conn with a healthy admin.Health
// response until the connection closes.
func serveHealth(conn *quicgo.Conn) {
	for {
		stream, err := conn.AcceptStream(context.Background())
		if err != nil {
			return
		}
		if _, _, requestID, _, err := wire.ReadRequest(stream); err == nil {
			writeHarnessResponse(stream, requestID, wire.StatusOK, mustMarshalHealthOK())
		}
		_ = stream.Close()
	}
}

// newConnectTransport builds a transport for addr with the given configured
// timeout, closed when the test ends.
func newConnectTransport(t *testing.T, addr string, clientCfg *tls.Config, timeout time.Duration) *Transport {
	t.Helper()
	tr, err := New(
		transport.WithAddress(addr),
		transport.WithTLSConfig(clientCfg),
		transport.WithTimeout(timeout),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

// TestTransport_Connect_RetriesRefusedAttemptWithinCallerBound pins that a
// connect whose first attempt fails is not the end of Connect: the next
// attempt, inside the caller's bound, reaches the server and passes the
// health check.
func TestTransport_Connect_RetriesRefusedAttemptWithinCallerBound(t *testing.T) {
	srv, clientCfg := startRefusingServer(t, 1)
	tr := newConnectTransport(t, srv.ln.Addr().String(), clientCfg, time.Minute)

	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v; want success on the attempt after the refused one", err)
	}
	if got := srv.accepted.Load(); got != 2 {
		t.Errorf("server accepted %d connections, want 2 (one refused, one healthy)", got)
	}
	if !tr.Healthy(context.Background()) {
		t.Error("Healthy after Connect = false, want true")
	}
}

// TestTransport_Connect_RetriesUntilRefusalsStop pins that Connect keeps
// retrying, with backoff, for as long as the caller's bound allows.
func TestTransport_Connect_RetriesUntilRefusalsStop(t *testing.T) {
	srv, clientCfg := startRefusingServer(t, 3)
	tr := newConnectTransport(t, srv.ln.Addr().String(), clientCfg, time.Minute)

	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v; want success after three refused attempts", err)
	}
	if got := srv.accepted.Load(); got != 4 {
		t.Errorf("server accepted %d connections, want 4", got)
	}
}

// TestTransport_Connect_GivesUpWhenCallerBoundIsSpent pins that the bound on
// Connect is the caller's configured timeout: with every attempt refused,
// Connect fails with a typed connection error carrying the deadline once that
// timeout is spent, having retried meanwhile.
func TestTransport_Connect_GivesUpWhenCallerBoundIsSpent(t *testing.T) {
	const bound = 500 * time.Millisecond
	srv, clientCfg := startRefusingServer(t, 1<<30)
	tr := newConnectTransport(t, srv.ln.Addr().String(), clientCfg, bound)

	start := time.Now()
	err := tr.Connect(context.Background())
	elapsed := time.Since(start)

	var connErr *transport.ConnectionError
	if !errors.As(err, &connErr) {
		t.Fatalf("Connect error = %T (%v), want *transport.ConnectionError", err, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Connect error = %v, want it to wrap context.DeadlineExceeded", err)
	}
	var appErr *quicgo.ApplicationError
	if !errors.As(err, &appErr) || appErr.ErrorCode != refusedCode {
		t.Errorf("Connect error = %v, want it to carry the last attempt's refusal", err)
	}
	if elapsed < bound {
		t.Errorf("Connect returned after %v, before the caller's %v bound", elapsed, bound)
	}
	if got := srv.accepted.Load(); got < 2 {
		t.Errorf("server accepted %d connections, want at least 2 (Connect retries)", got)
	}
}

// TestTransport_Connect_HonoursCallerContextDeadline pins that an SDK caller's
// context deadline bounds Connect when it is shorter than the configured
// timeout.
func TestTransport_Connect_HonoursCallerContextDeadline(t *testing.T) {
	srv, clientCfg := startRefusingServer(t, 1<<30)
	tr := newConnectTransport(t, srv.ln.Addr().String(), clientCfg, time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := tr.Connect(ctx)

	var connErr *transport.ConnectionError
	if !errors.As(err, &connErr) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Connect error = %v, want a *transport.ConnectionError wrapping context.DeadlineExceeded", err)
	}
}

// TestTransport_Connect_StopsOnCancellation pins that cancelling the caller's
// context ends the retry loop with the cancellation, not the deadline.
func TestTransport_Connect_StopsOnCancellation(t *testing.T) {
	srv, clientCfg := startRefusingServer(t, 1<<30)
	tr := newConnectTransport(t, srv.ln.Addr().String(), clientCfg, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-srv.refused
		cancel()
	}()

	err := tr.Connect(ctx)
	var connErr *transport.ConnectionError
	if !errors.As(err, &connErr) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Connect error = %v, want a *transport.ConnectionError wrapping context.Canceled", err)
	}
}

// TestTransport_Connect_SingleAttemptWithoutBound pins that Connect never
// loops unbounded: with no configured timeout and no caller deadline there
// is no bound to retry within, so one failed attempt is the answer.
func TestTransport_Connect_SingleAttemptWithoutBound(t *testing.T) {
	srv, clientCfg := startRefusingServer(t, 1<<30)
	tr := newConnectTransport(t, srv.ln.Addr().String(), clientCfg, 0)

	err := tr.Connect(context.Background())
	var connErr *transport.ConnectionError
	if !errors.As(err, &connErr) {
		t.Fatalf("Connect error = %T (%v), want *transport.ConnectionError", err, err)
	}
	if got := srv.accepted.Load(); got != 1 {
		t.Errorf("server accepted %d connections, want 1 (no retry without a bound)", got)
	}
}

// TestTransport_Connect_UntrustedServerIsNotRetried pins that a failure no
// retry can cure -- the server's certificate does not verify -- ends Connect
// at once instead of spending the caller's bound.
func TestTransport_Connect_UntrustedServerIsNotRetried(t *testing.T) {
	srv, clientCfg := startRefusingServer(t, 0)
	untrusting := clientCfg.Clone()
	untrusting.RootCAs = x509.NewCertPool()
	tr := newConnectTransport(t, srv.ln.Addr().String(), untrusting, time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err := tr.Connect(ctx)

	var verifyErr *tls.CertificateVerificationError
	if !errors.As(err, &verifyErr) {
		t.Fatalf("Connect error = %v, want it to carry *tls.CertificateVerificationError", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Connect error = %v, want no deadline: verification failure is not retried", err)
	}
}

// unfiredDeadlineContext is a context whose deadline has passed but whose
// timer has not fired yet: Err is still nil, the state a stream deadline set
// from the same instant can leave the caller's bound in.
type unfiredDeadlineContext struct {
	context.Context
	deadline time.Time
}

// Deadline returns the passed deadline.
func (c unfiredDeadlineContext) Deadline() (time.Time, bool) {
	return c.deadline, true
}

// TestBoundSpent_DeadlinePassedBeforeTimerFires pins that an attempt ending
// at the bound's deadline counts as cut short by it even before the context's
// own timer fires, so Connect keeps the earlier attempt's failure.
func TestBoundSpent_DeadlinePassedBeforeTimerFires(t *testing.T) {
	ctx := unfiredDeadlineContext{Context: context.Background(), deadline: time.Now()}
	if ctx.Err() != nil {
		t.Fatalf("ctx.Err() = %v, want nil (timer not fired)", ctx.Err())
	}
	if !boundSpent(ctx) {
		t.Error("boundSpent = false for a passed deadline, want true")
	}
}

// TestBoundSpent_LiveBound pins that a bound with time left, or no bound at
// all, is not spent: Connect records such an attempt's failure as the last.
func TestBoundSpent_LiveBound(t *testing.T) {
	live, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()

	for name, ctx := range map[string]context.Context{"live": live, "unbounded": context.Background()} {
		if boundSpent(ctx) {
			t.Errorf("%s: boundSpent = true, want false", name)
		}
	}
}

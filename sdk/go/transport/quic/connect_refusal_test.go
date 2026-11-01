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
	"errors"
	"sync/atomic"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// refusalMessage is the reason the answering server's refusals carry.
const refusalMessage = "auth/middleware: user cannot authenticate: alice in tenant t1 (lifecycle suspended)"

// answeringServer completes every handshake and answers every request with
// one fixed error status, as a qrdb server answers a caller its auth chain
// refuses: a revoked certificate, or an account suspended or disabled.
type answeringServer struct {
	ln     *quicgo.Listener
	status uint16
	// requests counts the requests the server answered.
	requests atomic.Int64
}

func startAnsweringServer(t *testing.T, status uint16) (*answeringServer, *tls.Config) {
	t.Helper()

	serverCfg, clientCfg := e2eTLS(t)
	ln, err := quicgo.ListenAddr("127.0.0.1:0", serverCfg, &quicgo.Config{MaxIdleTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &answeringServer{ln: ln, status: status}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept(context.Background())
			if err != nil {
				return
			}
			go s.answer(conn)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
	})
	return s, clientCfg
}

func (s *answeringServer) answer(conn *quicgo.Conn) {
	for {
		stream, err := conn.AcceptStream(context.Background())
		if err != nil {
			return
		}
		if _, _, requestID, _, err := wire.ReadRequest(stream); err == nil {
			s.requests.Add(1)
			writeHarnessResponse(stream, requestID, s.status, []byte(refusalMessage))
		}
		_ = stream.Close()
	}
}

// TestTransport_Connect_UnauthenticatedIsReturnedAtOnce: a server that
// refuses the caller's credential has answered. Connect returns that typed
// refusal after one attempt, well inside the caller's bound, rather than
// retrying it as a connectivity failure until the bound is spent.
func TestTransport_Connect_UnauthenticatedIsReturnedAtOnce(t *testing.T) {
	srv, clientCfg := startAnsweringServer(t, wire.StatusUnauthenticated)
	tr := newConnectTransport(t, srv.ln.Addr().String(), clientCfg, time.Minute)

	start := time.Now()
	err := tr.Connect(context.Background())
	elapsed := time.Since(start)

	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) || reqErr.StatusCode != int(wire.StatusUnauthenticated) {
		t.Fatalf("Connect error = %T %v, want a RequestError carrying UNAUTHENTICATED", err, err)
	}
	if want := "unauthenticated: " + refusalMessage; reqErr.Err.Error() != want {
		t.Errorf("refusal message %q, want %q", reqErr.Err.Error(), want)
	}
	var connErr *transport.ConnectionError
	if errors.As(err, &connErr) {
		t.Errorf("Connect error = %v, want the server's refusal, not a connection failure", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Connect error = %v, want no deadline: a refusal is not retried", err)
	}
	if got := srv.requests.Load(); got != 1 {
		t.Errorf("server answered %d requests, want 1: a refusal is not retried", got)
	}
	if elapsed > 5*time.Second {
		t.Errorf("Connect took %v against a one-minute bound, want the refusal at once", elapsed)
	}
}

// TestTransport_Connect_RateLimitedIsReturnedAtOnce pins that the
// client-address limiter's refusal (go-qrdb#1197) ends Connect at once
// rather than being retried until the bound is spent.
func TestTransport_Connect_RateLimitedIsReturnedAtOnce(t *testing.T) {
	srv, clientCfg := startAnsweringServer(t, wire.StatusResourceExhausted)
	tr := newConnectTransport(t, srv.ln.Addr().String(), clientCfg, time.Minute)

	err := tr.Connect(context.Background())

	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) || reqErr.StatusCode != int(wire.StatusResourceExhausted) {
		t.Fatalf("Connect error = %T %v, want a RequestError carrying RESOURCE_EXHAUSTED", err, err)
	}
	var connErr *transport.ConnectionError
	if errors.As(err, &connErr) {
		t.Errorf("Connect error = %v, want the server's refusal, not a connection failure", err)
	}
	if got := srv.requests.Load(); got != 1 {
		t.Errorf("server answered %d requests, want 1: a refusal is not retried", got)
	}
}

// TestTransport_Connect_ServerFailureIsStillRetried: an answer that says
// nothing about the caller -- an internal error on a node still starting --
// stays a connectivity failure, retried until the caller's bound.
func TestTransport_Connect_ServerFailureIsStillRetried(t *testing.T) {
	srv, clientCfg := startAnsweringServer(t, wire.StatusError)
	tr := newConnectTransport(t, srv.ln.Addr().String(), clientCfg, 1500*time.Millisecond)

	err := tr.Connect(context.Background())

	var connErr *transport.ConnectionError
	if !errors.As(err, &connErr) {
		t.Fatalf("Connect error = %T %v, want a ConnectionError", err, err)
	}
	var bound *connectBoundError
	if !errors.As(err, &bound) {
		t.Fatalf("Connect error = %v, want it to end at the caller's bound", err)
	}
	if got := srv.requests.Load(); got < 2 {
		t.Errorf("server answered %d requests, want the failure retried", got)
	}
}

// TestUnauthenticatedRefusal: only a RequestError carrying UNAUTHENTICATED,
// however deeply wrapped, is the server's refusal of the caller.
func TestCallerRefusal(t *testing.T) {
	refusal := &transport.RequestError{Method: "admin.Health", StatusCode: int(wire.StatusUnauthenticated), Err: errors.New("revoked")}
	limited := &transport.RequestError{Method: "admin.Health", StatusCode: int(wire.StatusResourceExhausted), Err: errors.New("rate limited")}
	cases := []struct {
		name    string
		err     error
		refusal *transport.RequestError
	}{
		{"bare refusal", refusal, refusal},
		{"refusal under the health probe", &transport.ConnectionError{Err: &dialError{Err: &healthCheckError{Err: refusal}}}, refusal},
		{"rate limited", limited, limited},
		{"rate limited under the health probe", &transport.ConnectionError{Err: &dialError{Err: &healthCheckError{Err: limited}}}, limited},
		{"permission denied", &transport.RequestError{StatusCode: int(wire.StatusPermissionDenied)}, nil},
		{"unavailable", &transport.RequestError{StatusCode: int(wire.StatusUnavailable)}, nil},
		{"not a request error", errors.New("dial failed"), nil},
		{"nil", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.refusal != nil
			got, ok := callerRefusal(tc.err)
			if ok != want {
				t.Fatalf("callerRefusal(%v) ok = %v, want %v", tc.err, ok, want)
			}
			if ok && got != tc.refusal {
				t.Fatalf("callerRefusal returned %v, want the wrapped refusal", got)
			}
			if retryableConnect(tc.err) == want && tc.err != nil {
				t.Fatalf("retryableConnect(%v) = %v, want %v", tc.err, want, !want)
			}
		})
	}
}

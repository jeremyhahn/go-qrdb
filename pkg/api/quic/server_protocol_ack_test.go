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
	"errors"
	"io"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
)

// TestServer_AcceptsOnlyProtocolV2 pins that the listener speaks one
// protocol: a client that does not offer wire.ProtocolV2 -- one offering only
// the retired "qrdb-quic", one offering only a protocol from a TLS config the
// listener might share with REST, or one offering nothing -- fails the
// handshake instead of connecting and being served under another label, and
// a client that offers ProtocolV2 among others gets ProtocolV2.
func TestServer_AcceptsOnlyProtocolV2(t *testing.T) {
	env := startIntegrationServer(t, &mockKVService{}, &mockAdminService{})

	dial := func(protos []string) (*quicgo.Conn, error) {
		cfg := env.clientCfg.Clone()
		cfg.NextProtos = protos
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return quicgo.DialAddr(ctx, env.srv.Addr(), cfg, &quicgo.Config{MaxIdleTimeout: 5 * time.Second})
	}

	refused := []struct {
		name   string
		protos []string
	}{
		{name: "the retired qrdb-quic", protos: []string{"qrdb-quic"}},
		{name: "a protocol a shared REST config carries", protos: []string{"h3"}},
		{name: "no ALPN at all", protos: nil},
	}
	for _, tt := range refused {
		t.Run(tt.name, func(t *testing.T) {
			conn, err := dial(tt.protos)
			if err == nil {
				_ = conn.CloseWithError(0, "test done")
				t.Fatalf("handshake offering %v succeeded, negotiated %q; want it refused", tt.protos, conn.ConnectionState().TLS.NegotiatedProtocol)
			}
		})
	}

	t.Run("ProtocolV2 among others", func(t *testing.T) {
		conn, err := dial([]string{"h3", "qrdb-quic", wire.ProtocolV2})
		if err != nil {
			t.Fatalf("dial offering ProtocolV2: %v", err)
		}
		defer func() { _ = conn.CloseWithError(0, "test done") }()
		if got := conn.ConnectionState().TLS.NegotiatedProtocol; got != wire.ProtocolV2 {
			t.Fatalf("negotiated %q, want %q", got, wire.ProtocolV2)
		}
	})
}

// requestWithoutClosing writes one request and reads its response while
// keeping the client's write side OPEN, returning the stream so the caller
// decides when (or whether) to acknowledge.
func requestWithoutClosing(t *testing.T, conn *quicgo.Conn, method string) *quicgo.Stream {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if err := wire.WriteRequest(stream, method, "", 1, nil); err != nil {
		t.Fatalf("write request: %v", err)
	}
	_, status, _, err := wire.ReadResponse(stream)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if status != wire.StatusOK {
		t.Fatalf("status = %s, want OK", wire.StatusName(status))
	}
	return stream
}

// assertResponseEndsPromptly asserts the server's side of stream has already
// ended after its response frame, so a client that reads to end-of-stream is
// not held for the acknowledgement wait.
func assertResponseEndsPromptly(t *testing.T, stream *quicgo.Stream) {
	t.Helper()

	_ = stream.SetReadDeadline(time.Now().Add(time.Second))
	var buf [1]byte
	if _, err := stream.Read(buf[:]); !errors.Is(err, io.EOF) {
		t.Fatalf("read past the response = %v, want io.EOF (the server's FIN travels with its response)", err)
	}
}

// stopAsync runs Stop in the background and returns a channel closed when it
// returns.
func stopAsync(srv *Server) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		srv.Stop()
		close(done)
	}()
	return done
}

const ackTestMethod = "test.ack"

// startAckTestServer starts a server whose dispatch table carries
// ackTestMethod, a handler that answers at once.
func startAckTestServer(t *testing.T) *integrationEnv {
	t.Helper()
	return startIntegrationServerWithHandlers(t, &mockKVService{}, &mockAdminService{}, map[string]methodHandler{
		ackTestMethod: func(context.Context, []byte) ([]byte, error) {
			return []byte("answered"), nil
		},
	})
}

// TestServer_ProtocolV2_HoldsTheRequestUntilTheClientAcknowledges pins the
// delivery confirmation go-qrdb#550 needs: a handler that has written its response still counts as in
// flight -- so Stop does not yet close the connection carrying that response
// -- until the client closes its side, and the client closing it (not the
// bounded timeout) is what releases it.
func TestServer_ProtocolV2_HoldsTheRequestUntilTheClientAcknowledges(t *testing.T) {
	env := startAckTestServer(t)

	conn := env.dialServer(t)
	stream := requestWithoutClosing(t, conn, ackTestMethod)
	assertResponseEndsPromptly(t, stream)

	stopDone := stopAsync(env.srv)
	select {
	case <-stopDone:
		t.Fatal("Stop returned before the client acknowledged the response")
	case <-time.After(250 * time.Millisecond):
	}

	acked := time.Now()
	if err := stream.Close(); err != nil {
		t.Fatalf("close (acknowledge): %v", err)
	}

	// Well inside quicResponseAckTimeout: the acknowledgement, not the bound,
	// ended the wait.
	limit := quicResponseAckTimeout / 2
	select {
	case <-stopDone:
		if waited := time.Since(acked); waited > limit {
			t.Fatalf("Stop took %v after the acknowledgement, want under %v", waited, limit)
		}
	case <-time.After(limit):
		t.Fatalf("Stop did not return within %v of the client's acknowledgement", limit)
	}
}

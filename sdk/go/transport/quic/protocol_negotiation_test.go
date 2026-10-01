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
	"io"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// streamServer is a minimal QUIC listener that hands every accepted stream to
// serve. It exists so a test can play a server whose stream discipline differs
// from pkg/api/quic's -- one that does not speak wire.ProtocolV2, or one that
// checks exactly when the client closes its side.
func streamServer(t *testing.T, serverCfg *tls.Config, serve func(*quicgo.Stream)) string {
	t.Helper()

	ln, err := quicgo.ListenAddr("127.0.0.1:0", serverCfg, &quicgo.Config{MaxIdleTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept(context.Background())
			if err != nil {
				return
			}
			go func() {
				for {
					stream, err := conn.AcceptStream(context.Background())
					if err != nil {
						return
					}
					go serve(stream)
				}
			}()
		}
	}()

	return ln.Addr().String()
}

// connectTo builds and connects an SDK transport to addr.
func connectTo(t *testing.T, addr string, clientCfg *tls.Config) *Transport {
	t.Helper()

	tr, err := New(
		transport.WithAddress(addr),
		transport.WithTLSConfig(clientCfg),
		transport.WithTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatalf("New transport: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := tr.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

// TestConnect_RefusesAServerThatDoesNotSelectProtocolV2 pins that the client
// speaks only wire.ProtocolV2: against a server predating it (which offers
// only the retired "qrdb-quic") the handshake fails, and against a peer that
// selects some other protocol a caller-supplied TLS config also offered,
// Connect refuses the connection rather than speaking ProtocolV2's stream
// discipline under another label.
func TestConnect_RefusesAServerThatDoesNotSelectProtocolV2(t *testing.T) {
	tests := []struct {
		name         string
		serverProtos []string
		clientProtos []string
		wantMismatch bool
	}{
		{name: "server predating ProtocolV2", serverProtos: []string{"qrdb-quic"}, clientProtos: nil},
		{name: "peer selecting another offered protocol", serverProtos: []string{"h3"}, clientProtos: []string{"h3"}, wantMismatch: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serverCfg, clientCfg := e2eTLS(t)
			serverCfg.NextProtos = tt.serverProtos
			clientCfg.NextProtos = tt.clientProtos
			addr := streamServer(t, serverCfg, func(stream *quicgo.Stream) {
				defer func() { _ = stream.Close() }()
				_, _, requestID, _, err := wire.ReadRequest(stream)
				if err != nil {
					return
				}
				_ = wire.WriteResponse(stream, requestID, wire.StatusOK, mustMarshalHealthOK())
			})

			tr, err := New(transport.WithAddress(addr), transport.WithTLSConfig(clientCfg), transport.WithTimeout(5*time.Second))
			if err != nil {
				t.Fatalf("New transport: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err = tr.Connect(ctx)
			if err == nil {
				_ = tr.Close()
				t.Fatal("Connect succeeded without negotiating ProtocolV2")
			}
			var connErr *transport.ConnectionError
			if !errors.As(err, &connErr) {
				t.Fatalf("Connect error = %T %v, want *transport.ConnectionError", err, err)
			}
			var mismatch *alpnMismatchError
			if got := errors.As(err, &mismatch); got != tt.wantMismatch {
				t.Fatalf("errors.As(alpnMismatchError) = %v, want %v (err = %v)", got, tt.wantMismatch, err)
			}
		})
	}
}

// finObservation records what a server saw of the client's
// request-stream half-close.
type finObservation struct {
	// finBeforeResponse is true when the client's FIN had already arrived
	// before the server wrote its response.
	finBeforeResponse bool
	// finAfterResponse is true when the client's FIN arrived after the
	// response was written, which is the ProtocolV2 acknowledgement.
	finAfterResponse bool
}

// TestCall_AcknowledgesResponseByClosingAfterReading pins the client half of
// wire.ProtocolV2: the client must
// NOT half-close before it has the response -- if it did, the FIN the server
// waits on would prove nothing -- and it MUST half-close once it has read the
// response, or the server would wait out its bound on every call.
func TestCall_AcknowledgesResponseByClosingAfterReading(t *testing.T) {
	serverCfg, clientCfg := e2eTLS(t)
	serverCfg.NextProtos = []string{wire.ProtocolV2}

	observed := make(chan finObservation, 8)
	addr := streamServer(t, serverCfg, func(stream *quicgo.Stream) {
		defer func() { _ = stream.Close() }()
		_, _, requestID, _, err := wire.ReadRequest(stream)
		if err != nil {
			stream.CancelWrite(0)
			return
		}

		var obs finObservation
		// Nothing follows the request frame, so a read either reports the
		// client's FIN (io.EOF) or times out because the side is still open.
		_ = stream.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		var probe [1]byte
		_, err = stream.Read(probe[:])
		obs.finBeforeResponse = errors.Is(err, io.EOF)

		_ = wire.WriteResponse(stream, requestID, wire.StatusOK, mustMarshalHealthOK())

		if !obs.finBeforeResponse {
			_ = stream.SetReadDeadline(time.Now().Add(3 * time.Second))
			_, err = stream.Read(probe[:])
			obs.finAfterResponse = errors.Is(err, io.EOF)
		}
		observed <- obs
	})

	tr := connectTo(t, addr, clientCfg)
	// Connect's own health probe was the first call; drain its observation
	// and make one more call through a database-scoped clone, which shares
	// the connection and must follow the same discipline.
	<-observed
	scoped, ok := tr.WithDatabase("orders").(*Transport)
	if !ok {
		t.Fatal("WithDatabase did not return *Transport")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := scoped.Health(ctx); err != nil {
		t.Fatalf("Health: %v", err)
	}

	select {
	case obs := <-observed:
		if obs.finBeforeResponse {
			t.Fatal("client closed its write side before reading the response ")
		}
		if !obs.finAfterResponse {
			t.Fatal("client never closed its write side after reading the response ")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server never recorded an observation")
	}
}

func TestAlpnMismatchError_NamesBothProtocols(t *testing.T) {
	tests := []struct {
		negotiated string
		want       string
	}{
		{negotiated: "h3", want: `quic transport: server selected ALPN protocol "h3", want "qrdb-quic/2"`},
		{negotiated: "", want: `quic transport: server selected ALPN protocol "", want "qrdb-quic/2"`},
	}
	for _, tt := range tests {
		if got := (&alpnMismatchError{Negotiated: tt.negotiated}).Error(); got != tt.want {
			t.Errorf("Error() = %q, want %q", got, tt.want)
		}
	}
}

// TestCall_HonoursContextWhileAwaitingTheResponse pins that a server which
// accepts a request and never answers cannot hold the caller past its
// context: the deadline and the cancellation both end the read, and the error
// reports the context's own reason so errors.Is works as on every other
// transport.
func TestCall_HonoursContextWhileAwaitingTheResponse(t *testing.T) {
	serverCfg, clientCfg := e2eTLS(t)
	serverCfg.NextProtos = []string{wire.ProtocolV2}
	addr := streamServer(t, serverCfg, func(stream *quicgo.Stream) {
		defer func() { _ = stream.Close() }()
		method, _, requestID, _, err := wire.ReadRequest(stream)
		if err != nil {
			return
		}
		if method == "admin.Health" {
			_ = wire.WriteResponse(stream, requestID, wire.StatusOK, mustMarshalHealthOK())
			return
		}
		// Never answer; wait for the client to give up.
		_, _ = io.Copy(io.Discard, stream)
	})
	tr := connectTo(t, addr, clientCfg)

	tests := []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		want error
	}{
		{
			name: "deadline",
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 200*time.Millisecond)
			},
			want: context.DeadlineExceeded,
		},
		{
			name: "cancellation",
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				time.AfterFunc(200*time.Millisecond, cancel)
				return ctx, cancel
			},
			want: context.Canceled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := tt.ctx()
			defer cancel()

			started := time.Now()
			_, err := tr.call(ctx, "test.hang", nil)
			if !errors.Is(err, tt.want) {
				t.Fatalf("call error = %v, want errors.Is %v", err, tt.want)
			}
			// The transport's own 5s timeout must not be what ended it.
			if waited := time.Since(started); waited > 2*time.Second {
				t.Fatalf("call returned after %v, want shortly after the context ended", waited)
			}
		})
	}
}

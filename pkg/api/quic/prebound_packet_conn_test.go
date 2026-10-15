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
	"net"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"
)

// TestWithPacketConn_ServesOnTheHandedOverSocket pins go-qrdb#693 for QUIC:
// the server serves on the socket it was handed and never binds its
// configured address, so the sealed-window handover can keep the port held.
func TestWithPacketConn_ServesOnTheHandedOverSocket(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	bundle := generateIntegrationTLS(t)
	// A configured address that cannot bind proves it is not used.
	srv := NewServer(nil, nil, "256.0.0.1:1", WithTLS(bundle.serverCfg), WithPacketConn(pc))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)

	if got, want := srv.Addr(), pc.LocalAddr().String(); got != want {
		t.Fatalf("Addr() = %q, want the handed-over socket %q", got, want)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := quicgo.DialAddr(ctx, pc.LocalAddr().String(), bundle.clientCfg, &quicgo.Config{})
	if err != nil {
		t.Fatalf("handshake on the served socket: %v", err)
	}
	_ = conn.CloseWithError(0, "test done")
}

// TestWithPacketConn_RefusedStartClosesIt pins the ownership contract: a
// Start that refuses to serve still owns the handed socket and closes it,
// rather than leaking a bound port nobody will ever serve or release.
func TestWithPacketConn_RefusedStartClosesIt(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	srv := NewServer(nil, nil, "127.0.0.1:0", WithPacketConn(pc))
	err = srv.Start()
	var want *StartError
	if !errors.As(err, &want) {
		t.Fatalf("Start = %v, want *StartError for the missing TLS config", err)
	}
	if err := pc.SetReadDeadline(time.Now()); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("socket still open after a refused Start (SetReadDeadline err = %v)", err)
	}
}

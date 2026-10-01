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

package server

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
)

// staleTimerWindow is how long the next lifetime keeps being dialled after the
// previous one ended. It is NOT a budget a correct build must meet: a correct
// build answers every dial whatever its length. It exists so the defect gets
// the chance to show — the ended lifetime's quic-go transport moves the shared
// socket's read deadline into the past about three probe timeouts after it
// closed a connection, which on loopback is a few hundred milliseconds at
// most. A loaded machine can only delay the timer inside the window, never
// fail a correct build.
const staleTimerWindow = 1500 * time.Millisecond

// TestLifetimePacketConn_EndedLifetimeCannotMoveTheDeadline is the regression
// for a lifetime ending with a live QUIC connection: quic-go keeps the closed
// connection's timer running, and when it fires it sets the socket's read
// deadline to now. The socket belongs to the next lifetime by then, so that
// call must not reach it — every dial of the next lifetime, for longer than
// three probe timeouts, must be accepted.
func TestLifetimePacketConn_EndedLifetimeCannotMoveTheDeadline(t *testing.T) {
	for _, tc := range []struct {
		name string
		wrap func(*net.UDPConn) net.PacketConn
	}{
		{name: "udp", wrap: func(u *net.UDPConn) net.PacketConn { return newLifetimePacketConn(u) }},
		{name: "generic", wrap: func(u *net.UDPConn) net.PacketConn { return newLifetimePacketConn(noUDP{u}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			udp := listenUDP(t)
			srvTLS := selfSignedTLSConfig(t)
			srvTLS.NextProtos = []string{"qrdb-test"}
			cliTLS := &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"qrdb-test"}} //nolint:gosec // self-signed loopback test server
			ctx, cancel := context.WithTimeout(context.Background(), preboundBound)
			defer cancel()

			// Lifetime 1 ends with a connection still open, as
			// http3.Server.Close leaves one.
			first := tc.wrap(udp)
			ln1, err := quic.ListenEarly(first, srvTLS, &quic.Config{})
			if err != nil {
				t.Fatalf("listen (first lifetime): %v", err)
			}
			accepted := make(chan *quic.Conn, 1)
			go func() {
				if c, err := ln1.Accept(ctx); err == nil {
					accepted <- c
				}
			}()
			client, err := quic.DialAddr(ctx, udp.LocalAddr().String(), cliTLS, &quic.Config{})
			if err != nil {
				t.Fatalf("dial the first lifetime: %v", err)
			}
			serverSide := <-accepted
			_ = ln1.Close()
			_ = client.CloseWithError(0, "")
			// The server side has seen the close — and started the timer
			// that outlives the lifetime — once its context is done.
			<-serverSide.Context().Done()
			if err := first.Close(); err != nil {
				t.Fatalf("end the first lifetime: %v", err)
			}

			second := tc.wrap(udp)
			ln2, err := quic.ListenEarly(second, srvTLS, &quic.Config{})
			if err != nil {
				t.Fatalf("listen (second lifetime): %v", err)
			}
			t.Cleanup(func() { _ = ln2.Close(); _ = second.Close() })
			go func() {
				for {
					c, err := ln2.Accept(ctx)
					if err != nil {
						return
					}
					go func() { <-c.Context().Done() }()
				}
			}()

			for start := time.Now(); time.Since(start) < staleTimerWindow; {
				dctx, dcancel := context.WithTimeout(ctx, 2*time.Second)
				c, err := quic.DialAddr(dctx, udp.LocalAddr().String(), cliTLS, &quic.Config{})
				dcancel()
				if err != nil {
					t.Fatalf("the second lifetime stopped answering %v after it began: %v", time.Since(start), err)
				}
				_ = c.CloseWithError(0, "")
				time.Sleep(50 * time.Millisecond)
			}
		})
	}
}

// TestLifetimePacketConn_DeadlineSettersInertAfterEnd pins the mechanism
// directly: once a lifetime has ended, its deadline setters leave the shared
// socket alone, so a datagram still reaches the next lifetime's read.
func TestLifetimePacketConn_DeadlineSettersInertAfterEnd(t *testing.T) {
	for name, wrap := range map[string]func(*net.UDPConn) net.PacketConn{
		"udp":     func(u *net.UDPConn) net.PacketConn { return newLifetimePacketConn(u) },
		"generic": func(u *net.UDPConn) net.PacketConn { return newLifetimePacketConn(noUDP{u}) },
	} {
		t.Run(name, func(t *testing.T) {
			udp := listenUDP(t)
			ended := wrap(udp)
			if err := ended.Close(); err != nil {
				t.Fatalf("lifetime Close: %v", err)
			}
			past := time.Now().Add(-time.Second)
			if err := ended.SetReadDeadline(past); err != nil {
				t.Fatalf("SetReadDeadline on an ended lifetime: %v", err)
			}
			if err := ended.(interface{ SetDeadline(time.Time) error }).SetDeadline(past); err != nil {
				t.Fatalf("SetDeadline on an ended lifetime: %v", err)
			}

			next := wrap(udp)
			defer func() { _ = next.Close() }()
			got := make(chan error, 1)
			go func() {
				_, _, err := next.ReadFrom(make([]byte, 16))
				got <- err
			}()
			if _, err := udp.WriteTo([]byte("x"), udp.LocalAddr()); err != nil {
				t.Fatalf("send: %v", err)
			}
			select {
			case err := <-got:
				if err != nil {
					t.Fatalf("the next lifetime's read failed: %v (an ended lifetime moved the deadline)", err)
				}
			case <-time.After(preboundBound):
				t.Fatal("the next lifetime never received the datagram")
			}
		})
	}
}

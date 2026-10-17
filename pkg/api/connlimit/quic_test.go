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

package connlimit

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"
)

// generateQUICTestTLS returns a self-signed ECDSA P-256 server tls.Config
// (valid for 127.0.0.1) and a client tls.Config trusting it, both carrying
// the ALPN quic-go.Listen requires. The certificate acts as its own CA.
func generateQUICTestTLS(t *testing.T) (serverCfg, clientCfg *tls.Config) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("generate ECDSA key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "connlimit-quic-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	certDER, err := x509.CreateCertificate(crand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	parsed, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}

	tlsCert := tls.Certificate{Certificate: [][]byte{certDER}, PrivateKey: key, Leaf: parsed}
	caPool := x509.NewCertPool()
	caPool.AddCert(parsed)

	serverCfg = &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		NextProtos:   []string{"connlimit-quic-test"},
		MinVersion:   tls.VersionTLS13,
	}
	clientCfg = &tls.Config{
		RootCAs:    caPool,
		NextProtos: []string{"connlimit-quic-test"},
		MinVersion: tls.VersionTLS13,
	}
	return serverCfg, clientCfg
}

// dialQUIC opens n QUIC connections to addr and returns them still open;
// the caller must close them.
func dialQUIC(t *testing.T, addr string, clientCfg *tls.Config, n int) []*quicgo.Conn {
	t.Helper()
	conns := make([]*quicgo.Conn, 0, n)
	for i := 0; i < n; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		conn, err := quicgo.DialAddr(ctx, addr, clientCfg, nil)
		cancel()
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		conns = append(conns, conn)
	}
	return conns
}

// loopbackPeerAddr returns the address the server sees for client
// connection c. A dialer binds the unspecified address, so c's local
// address is [::]:port while the server observes the loopback source
// 127.0.0.1:port; only the port identifies the connection.
func loopbackPeerAddr(t *testing.T, c *quicgo.Conn) string {
	t.Helper()
	local, ok := c.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("client local address is %T, want *net.UDPAddr", c.LocalAddr())
	}
	return (&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: local.Port}).String()
}

func TestQUICListenerRefusesOverLimitFromSamePeer(t *testing.T) {
	serverCfg, clientCfg := generateQUICTestTLS(t)

	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	defer udpConn.Close()

	raw, err := quicgo.Listen(udpConn, serverCfg, nil)
	if err != nil {
		t.Fatalf("quic listen: %v", err)
	}
	defer raw.Close()

	ln := NewQUICListener(raw, 2)

	accepted := make(chan *quicgo.Conn, 8)
	go func() {
		for {
			c, err := ln.Accept(context.Background())
			if err != nil {
				return
			}
			accepted <- c
		}
	}()

	conns := dialQUIC(t, udpConn.LocalAddr().String(), clientCfg, 3)
	defer func() {
		for _, c := range conns {
			_ = c.CloseWithError(0, "test done")
		}
	}()

	// Exactly two of the three connections from this peer should be
	// admitted and the remaining one refused at Accept with the over-cap
	// error code. Which one is refused is NOT fixed by dial order: the
	// server queues a connection for Accept when ITS side of the handshake
	// completes, which can trail the client's DialAddr returning, so the
	// first connection dialed can be the last one the listener sees. Under
	// GODEBUG=fips140=only the module's lazy self-tests and pairwise
	// consistency checks slow the early handshakes enough to reorder them
	// routinely (go-qrdb#608), and a test that assumed conns[2] failed
	// there. The refused connection is therefore identified by elimination
	// against the admitted ones' addresses.
	admittedAddrs := make(map[string]bool, 2)
	timeout := time.After(3 * time.Second)
	for len(admittedAddrs) < 2 {
		select {
		case c := <-accepted:
			admittedAddrs[c.RemoteAddr().String()] = true
		case <-timeout:
			t.Fatalf("timed out waiting for admitted connections, got %d", len(admittedAddrs))
		}
	}

	var refused []*quicgo.Conn
	for _, c := range conns {
		if !admittedAddrs[loopbackPeerAddr(t, c)] {
			refused = append(refused, c)
		}
	}
	if len(refused) != 1 {
		t.Fatalf("expected exactly one connection outside the admitted set, got %d", len(refused))
	}
	third := refused[0]
	select {
	case <-third.Context().Done():
	case <-time.After(3 * time.Second):
		t.Fatal("the connection over the cap was not refused within timeout")
	}

	var appErr *quicgo.ApplicationError
	err = context.Cause(third.Context())
	if !errors.As(err, &appErr) {
		t.Fatalf("expected an ApplicationError closing the refused connection, got %v (%T)", err, err)
	}
	if appErr.ErrorCode != QUICOverCapErrorCode {
		t.Fatalf("expected error code %d, got %d", QUICOverCapErrorCode, appErr.ErrorCode)
	}
}

func TestQUICListenerReleasesSlotOnConnectionClose(t *testing.T) {
	serverCfg, clientCfg := generateQUICTestTLS(t)

	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	defer udpConn.Close()

	raw, err := quicgo.Listen(udpConn, serverCfg, nil)
	if err != nil {
		t.Fatalf("quic listen: %v", err)
	}
	defer raw.Close()

	ln := NewQUICListener(raw, 1)

	accepted := make(chan *quicgo.Conn, 8)
	go func() {
		for {
			c, err := ln.Accept(context.Background())
			if err != nil {
				return
			}
			accepted <- c
		}
	}()

	// First connection claims the (only) slot for this peer.
	first := dialQUIC(t, udpConn.LocalAddr().String(), clientCfg, 1)[0]
	select {
	case <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("first connection was not admitted")
	}

	// A second connection from the same peer is refused while the first
	// is still open.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	blocked, dialErr := quicgo.DialAddr(ctx, udpConn.LocalAddr().String(), clientCfg, nil)
	cancel()
	if dialErr == nil {
		select {
		case <-blocked.Context().Done():
		case <-time.After(3 * time.Second):
			t.Fatal("second connection should have been refused while first is open")
		}
		_ = blocked.CloseWithError(0, "test done")
	}

	// Closing the first connection releases its slot: a subsequent
	// connection from the same peer is admitted.
	_ = first.CloseWithError(0, "releasing slot")

	// context.AfterFunc(conn.Context(), release) runs asynchronously; give
	// it a moment to observe the connection's context ending before the
	// next dial, which is what the slot release is racing against.
	time.Sleep(200 * time.Millisecond)

	next := dialQUIC(t, udpConn.LocalAddr().String(), clientCfg, 1)[0]
	defer func() { _ = next.CloseWithError(0, "test done") }()

	select {
	case <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("connection after release was not admitted")
	}
}

func TestQUICListenerUnlimitedWhenMaxNonPositive(t *testing.T) {
	serverCfg, clientCfg := generateQUICTestTLS(t)

	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	defer udpConn.Close()

	raw, err := quicgo.Listen(udpConn, serverCfg, nil)
	if err != nil {
		t.Fatalf("quic listen: %v", err)
	}
	defer raw.Close()

	ln := NewQUICListener(raw, 0)

	accepted := make(chan *quicgo.Conn, 8)
	go func() {
		for {
			c, err := ln.Accept(context.Background())
			if err != nil {
				return
			}
			accepted <- c
		}
	}()

	conns := dialQUIC(t, udpConn.LocalAddr().String(), clientCfg, 5)
	defer func() {
		for _, c := range conns {
			_ = c.CloseWithError(0, "test done")
		}
	}()

	for i := 0; i < 5; i++ {
		select {
		case <-accepted:
		case <-time.After(3 * time.Second):
			t.Fatalf("connection %d was not admitted under an unlimited cap", i)
		}
	}
}

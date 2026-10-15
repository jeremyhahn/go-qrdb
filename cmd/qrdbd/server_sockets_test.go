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

package main

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/jeremyhahn/go-qrdb/internal/netport"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
)

// socketsConfig is a server configuration with REST and gRPC on the given
// ports of the test bind host.
func socketsConfig(adminPort, grpcPort int, rest, grpc bool) *qrdbconfig.Config {
	return &qrdbconfig.Config{
		Server:    qrdbconfig.ServerConfig{Host: testBindHost, AdminPort: adminPort, GRPCPort: grpcPort},
		Protocols: qrdbconfig.ProtocolsConfig{REST: rest, GRPC: grpc},
	}
}

// boundAddrs returns the local address of every socket opts hands over.
func boundAddrs(t *testing.T, opts []server.ServerOption) []net.Addr {
	t.Helper()
	var addrs []net.Addr
	for _, sock := range server.PreboundSockets(opts...) {
		switch s := sock.(type) {
		case net.Listener:
			addrs = append(addrs, s.Addr())
		case net.PacketConn:
			addrs = append(addrs, s.LocalAddr())
		default:
			t.Fatalf("pre-bound socket %T is neither a listener nor a packet conn", sock)
		}
	}
	return addrs
}

// requireBindable fails the test unless addr can be bound on network now,
// which is the observable difference between a socket that was closed and one
// still held.
func requireBindable(t *testing.T, network, addr string) {
	t.Helper()
	var err error
	if network == netport.NetworkUDP {
		var pc net.PacketConn
		if pc, err = net.ListenPacket(network, addr); err == nil {
			_ = pc.Close()
		}
	} else {
		var ln net.Listener
		if ln, err = net.Listen(network, addr); err == nil {
			_ = ln.Close()
		}
	}
	if err != nil {
		t.Fatalf("%s %s is still held: %v", network, addr, err)
	}
}

func TestBindServerSockets_BindsRESTPairAndGRPC(t *testing.T) {
	opts, err := bindServerSockets(socketsConfig(0, 0, true, true))
	if err != nil {
		t.Fatalf("bindServerSockets: %v", err)
	}
	addrs := boundAddrs(t, opts)
	closeServerSockets(opts)

	if len(addrs) != 3 {
		t.Fatalf("bound %d sockets, want REST TCP, REST UDP and gRPC TCP: %v", len(addrs), addrs)
	}
	restTCP, restUDP, grpcTCP := addrs[0], addrs[1], addrs[2]
	if restTCP.Network() != netport.NetworkTCP || restUDP.Network() != netport.NetworkUDP || grpcTCP.Network() != netport.NetworkTCP {
		t.Fatalf("socket networks = %s, %s, %s; want tcp, udp, tcp",
			restTCP.Network(), restUDP.Network(), grpcTCP.Network())
	}
	// The HTTP/3 socket must sit on the TCP listener's host and port, or the
	// REST server refuses it; with port 0 that port is the kernel's choice.
	if restTCP.String() != restUDP.String() {
		t.Fatalf("HTTP/3 socket on %s, REST listener on %s: they must share host and port", restUDP, restTCP)
	}
	for _, a := range []net.Addr{restTCP, grpcTCP} {
		if host, _, _ := net.SplitHostPort(a.String()); host != testBindHost {
			t.Fatalf("socket bound on %s, want host %s", a, testBindHost)
		}
	}
	requireBindable(t, netport.NetworkTCP, restTCP.String())
	requireBindable(t, netport.NetworkUDP, restUDP.String())
	requireBindable(t, netport.NetworkTCP, grpcTCP.String())
}

func TestBindServerSockets_DisabledProtocolsBindNothing(t *testing.T) {
	opts, err := bindServerSockets(socketsConfig(0, 0, false, false))
	if err != nil {
		t.Fatalf("bindServerSockets: %v", err)
	}
	if len(opts) != 0 {
		t.Fatalf("bound %d options with REST and gRPC disabled; a pre-bound socket for a "+
			"disabled protocol makes server.Start refuse to serve", len(opts))
	}

	opts, err = bindServerSockets(socketsConfig(0, 0, false, true))
	if err != nil {
		t.Fatalf("bindServerSockets (gRPC only): %v", err)
	}
	defer closeServerSockets(opts)
	if got := len(server.PreboundSockets(opts...)); got != 1 {
		t.Fatalf("bound %d sockets with only gRPC enabled, want 1", got)
	}
}

func TestBindServerSockets_GRPCBindFailureReleasesREST(t *testing.T) {
	busy, err := net.Listen(netport.NetworkTCP, net.JoinHostPort(testBindHost, "0"))
	if err != nil {
		t.Fatalf("occupy a port: %v", err)
	}
	defer func() { _ = busy.Close() }()
	busyPort := busy.Addr().(*net.TCPAddr).Port

	adminPort, err := netport.FreePortOn(testBindHost, netport.NetworkTCP, netport.NetworkUDP)
	if err != nil {
		t.Fatalf("admin port: %v", err)
	}

	opts, err := bindServerSockets(socketsConfig(adminPort, busyPort, true, true))
	if err == nil {
		closeServerSockets(opts)
		t.Fatal("bindServerSockets succeeded on an occupied gRPC port")
	}
	var bindErr *ServerSocketBindError
	if !errors.As(err, &bindErr) {
		t.Fatalf("error %T (%v), want *ServerSocketBindError", err, err)
	}
	if bindErr.Protocol != "grpc" || bindErr.Network != netport.NetworkTCP {
		t.Fatalf("failure attributed to %s %s, want grpc tcp", bindErr.Protocol, bindErr.Network)
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("error %v does not unwrap to EADDRINUSE", err)
	}
	// The REST pair bound before the failure must not leak.
	admin := net.JoinHostPort(testBindHost, strconv.Itoa(adminPort))
	requireBindable(t, netport.NetworkTCP, admin)
	requireBindable(t, netport.NetworkUDP, admin)
}

func TestBindServerSockets_HTTP3BindFailureReleasesTCP(t *testing.T) {
	// A port free on both networks, then taken on UDP only: the TCP listener
	// binds and the HTTP/3 socket beside it cannot.
	port, err := netport.FreePortOn(testBindHost, netport.NetworkTCP, netport.NetworkUDP)
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	addr := net.JoinHostPort(testBindHost, strconv.Itoa(port))
	busy, err := net.ListenPacket(netport.NetworkUDP, addr)
	if err != nil {
		t.Fatalf("occupy UDP %s: %v", addr, err)
	}
	defer func() { _ = busy.Close() }()

	opts, err := bindServerSockets(socketsConfig(port, 0, true, false))
	if err == nil {
		closeServerSockets(opts)
		t.Fatalf("bindServerSockets succeeded with UDP %s occupied", addr)
	}
	var bindErr *ServerSocketBindError
	if !errors.As(err, &bindErr) {
		t.Fatalf("error %T (%v), want *ServerSocketBindError", err, err)
	}
	if bindErr.Protocol != "rest" || bindErr.Network != netport.NetworkUDP || bindErr.Addr != addr {
		t.Fatalf("failure attributed to %s %s %s, want rest udp %s",
			bindErr.Protocol, bindErr.Network, bindErr.Addr, addr)
	}
	// The TCP listener bound before the failure must not leak.
	requireBindable(t, netport.NetworkTCP, addr)
}

func TestServerSockets_OptionsBindOnceAndCloseReleases(t *testing.T) {
	var sockets serverSockets
	cfg := socketsConfig(0, 0, true, true)

	first, err := sockets.options(cfg)
	if err != nil {
		t.Fatalf("options: %v", err)
	}
	second, err := sockets.options(cfg)
	if err != nil {
		t.Fatalf("options (second call): %v", err)
	}
	firstAddrs, secondAddrs := boundAddrs(t, first), boundAddrs(t, second)
	if len(firstAddrs) != 3 || len(secondAddrs) != len(firstAddrs) {
		t.Fatalf("first call bound %v, second returned %v", firstAddrs, secondAddrs)
	}
	for i := range firstAddrs {
		if firstAddrs[i].String() != secondAddrs[i].String() {
			t.Fatalf("second call returned %v, not the sockets the first bound (%v): every "+
				"server of one boot must serve on the same sockets", secondAddrs, firstAddrs)
		}
	}
	// Held between the calls, as they are between two server lifetimes.
	if ln, err := net.Listen(netport.NetworkTCP, firstAddrs[0].String()); err == nil {
		_ = ln.Close()
		t.Fatalf("REST port %s was free while serverSockets held it", firstAddrs[0])
	}

	sockets.Close()
	requireBindable(t, netport.NetworkTCP, firstAddrs[0].String())
	requireBindable(t, netport.NetworkUDP, firstAddrs[1].String())
	requireBindable(t, netport.NetworkTCP, firstAddrs[2].String())
}

func TestServerSockets_CloseIsSafeUnboundAndTwice(t *testing.T) {
	var sockets serverSockets
	sockets.Close()

	if _, err := sockets.options(socketsConfig(0, 0, true, false)); err != nil {
		t.Fatalf("options: %v", err)
	}
	sockets.Close()
	sockets.Close()
	if sockets.bound || sockets.opts != nil {
		t.Fatal("Close left the sockets recorded as bound")
	}
}

func TestServerSockets_OptionsReportsBindFailure(t *testing.T) {
	busy, err := net.Listen(netport.NetworkTCP, net.JoinHostPort(testBindHost, "0"))
	if err != nil {
		t.Fatalf("occupy a port: %v", err)
	}
	defer func() { _ = busy.Close() }()

	var sockets serverSockets
	defer sockets.Close()
	_, err = sockets.options(socketsConfig(busy.Addr().(*net.TCPAddr).Port, 0, true, false))
	var bindErr *ServerSocketBindError
	if !errors.As(err, &bindErr) || bindErr.Protocol != "rest" || bindErr.Network != netport.NetworkTCP {
		t.Fatalf("options on an occupied admin port = %v, want a rest tcp *ServerSocketBindError", err)
	}
	if sockets.bound {
		t.Fatal("a failed bind left serverSockets recorded as bound")
	}
}

func TestServerSocketBindError_ErrorAndUnwrap(t *testing.T) {
	cause := syscall.EADDRINUSE
	err := &ServerSocketBindError{Protocol: "rest", Network: "udp", Addr: "127.0.0.1:443", Err: cause}
	msg := err.Error()
	for _, want := range []string{"rest", "udp", "127.0.0.1:443", cause.Error()} {
		if !strings.Contains(msg, want) {
			t.Fatalf("Error() = %q, missing %q", msg, want)
		}
	}
	if !errors.Is(err, cause) || err.Unwrap() != error(cause) {
		t.Fatalf("Unwrap does not reach the cause")
	}
}

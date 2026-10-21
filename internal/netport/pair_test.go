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

package netport

import (
	"errors"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
)

// stubBinds replaces ListenPair's binds for one test and restores them after.
func stubBinds(t *testing.T, l func(network, addr string) (net.Listener, error), lp func(network, addr string) (net.PacketConn, error)) {
	t.Helper()
	origListen, origListenPacket := listen, listenPacket
	t.Cleanup(func() { listen, listenPacket = origListen, origListenPacket })
	if l != nil {
		listen = l
	}
	if lp != nil {
		listenPacket = lp
	}
}

// addrInUse is the error a kernel bind returns for a number another socket
// holds, in the shape net.ListenPacket wraps it.
func addrInUse() error {
	return &net.OpError{Op: "listen", Net: NetworkUDP, Err: os.NewSyscallError("bind", syscall.EADDRINUSE)}
}

// recordListens wraps net.Listen and keeps every listener it hands out.
func recordListens(t *testing.T) *[]net.Listener {
	t.Helper()
	var got []net.Listener
	stubBinds(t, func(network, addr string) (net.Listener, error) {
		ln, err := net.Listen(network, addr)
		if err == nil {
			got = append(got, ln)
		}
		return ln, err
	}, nil)
	return &got
}

// assertClosed fails unless ln has been closed.
func assertClosed(t *testing.T, ln net.Listener) {
	t.Helper()
	if _, err := ln.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("listener %s: Accept err = %v, want net.ErrClosed (a rejected pair must release its TCP half)", ln.Addr(), err)
	}
}

func TestListenPair_HoldsBothHalvesOnOnePort(t *testing.T) {
	ln, pc, err := ListenPair("127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPair: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close(); _ = pc.Close() })
	if ln.Addr().String() != pc.LocalAddr().String() {
		t.Fatalf("TCP %s and UDP %s are not one host and port", ln.Addr(), pc.LocalAddr())
	}
	if strings.HasSuffix(ln.Addr().String(), ":0") {
		t.Fatalf("TCP %s: the kernel did not draw a port", ln.Addr())
	}
}

func TestListenPair_ReadsTheUDPAddressBackFromTheTCPListener(t *testing.T) {
	var asked []string
	stubBinds(t, nil, func(network, addr string) (net.PacketConn, error) {
		asked = append(asked, network+" "+addr)
		return net.ListenPacket(network, addr)
	})
	ln, pc, err := ListenPair("localhost:0")
	if err != nil {
		t.Fatalf("ListenPair: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close(); _ = pc.Close() })
	if want := []string{"udp " + ln.Addr().String()}; len(asked) != 1 || asked[0] != want[0] {
		t.Fatalf("UDP binds = %q, want %q: the UDP half must bind what the TCP half resolved to", asked, want)
	}
}

func TestListenPair_RedrawsTheWholePairWhenUDPIsTaken(t *testing.T) {
	listens := recordListens(t)
	refusals := 2
	stubBinds(t, nil, func(network, addr string) (net.PacketConn, error) {
		if refusals > 0 {
			refusals--
			return nil, addrInUse()
		}
		return net.ListenPacket(network, addr)
	})
	ln, pc, err := ListenPair("127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPair: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close(); _ = pc.Close() })
	if got := len(*listens); got != 3 {
		t.Fatalf("TCP binds = %d, want 3 (two refused pairs, one held)", got)
	}
	for _, rejected := range (*listens)[:2] {
		assertClosed(t, rejected)
	}
	if (*listens)[2] != ln {
		t.Fatal("the returned listener is not the last one bound")
	}
	if ln.Addr().String() != pc.LocalAddr().String() {
		t.Fatalf("TCP %s and UDP %s are not one host and port", ln.Addr(), pc.LocalAddr())
	}
}

func TestListenPair_GivesUpAfterTheAttemptBound(t *testing.T) {
	listens := recordListens(t)
	stubBinds(t, nil, func(string, string) (net.PacketConn, error) { return nil, addrInUse() })
	_, _, err := ListenPair("127.0.0.1:0")
	var pairErr *ListenPairError
	if !errors.As(err, &pairErr) {
		t.Fatalf("err = %T %v, want *ListenPairError", err, err)
	}
	if pairErr.Network != NetworkUDP || pairErr.Attempts != freePortAttempts {
		t.Fatalf("err = %+v, want network %q after %d attempts", pairErr, NetworkUDP, freePortAttempts)
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("err = %v, want it to wrap EADDRINUSE", err)
	}
	if got := len(*listens); got != freePortAttempts {
		t.Fatalf("TCP binds = %d, want %d", got, freePortAttempts)
	}
	for _, rejected := range *listens {
		assertClosed(t, rejected)
	}
}

func TestListenPair_DoesNotRedrawAFailureOtherThanAddressInUse(t *testing.T) {
	listens := recordListens(t)
	refused := &net.OpError{Op: "listen", Net: NetworkUDP, Err: os.NewSyscallError("bind", syscall.EACCES)}
	stubBinds(t, nil, func(string, string) (net.PacketConn, error) { return nil, refused })
	_, _, err := ListenPair("127.0.0.1:0")
	var pairErr *ListenPairError
	if !errors.As(err, &pairErr) || pairErr.Network != NetworkUDP || pairErr.Attempts != 1 {
		t.Fatalf("err = %v, want a UDP *ListenPairError after 1 attempt", err)
	}
	if !errors.Is(err, refused) {
		t.Fatalf("err = %v, want it to wrap %v", err, refused)
	}
	if len(*listens) != 1 {
		t.Fatalf("TCP binds = %d, want 1", len(*listens))
	}
	assertClosed(t, (*listens)[0])
}

func TestListenPair_BindsANamedPortOnce(t *testing.T) {
	held, err := net.ListenPacket(NetworkUDP, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("hold UDP: %v", err)
	}
	t.Cleanup(func() { _ = held.Close() })
	named := held.LocalAddr().String()
	listens := recordListens(t)
	// The TCP half is a kernel-drawn stand-in so the test does not depend on
	// the named number also being free on TCP; the UDP half is the real bind
	// against the socket held above.
	stubBinds(t, func(network, _ string) (net.Listener, error) {
		ln, err := net.Listen(network, "127.0.0.1:0")
		if err == nil {
			*listens = append(*listens, ln)
		}
		return &fixedAddrListener{Listener: ln, addr: held.LocalAddr()}, err
	}, nil)
	_, _, err = ListenPair(named)
	var pairErr *ListenPairError
	if !errors.As(err, &pairErr) || pairErr.Network != NetworkUDP || pairErr.Attempts != 1 || pairErr.Addr != named {
		t.Fatalf("err = %v, want a UDP *ListenPairError for %s after 1 attempt", err, named)
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("err = %v, want it to wrap EADDRINUSE", err)
	}
	if len(*listens) != 1 {
		t.Fatalf("TCP binds = %d, want 1: a named port cannot be redrawn", len(*listens))
	}
	assertClosed(t, (*listens)[0])
}

func TestListenPair_ReportsATCPFailureAsTCP(t *testing.T) {
	for _, addr := range []string{"256.0.0.1:0", "no-port"} {
		t.Run(addr, func(t *testing.T) {
			ln, pc, err := ListenPair(addr)
			var pairErr *ListenPairError
			if !errors.As(err, &pairErr) || pairErr.Network != NetworkTCP || pairErr.Addr != addr || pairErr.Attempts != 1 {
				t.Fatalf("err = %v, want a TCP *ListenPairError for %q after 1 attempt", err, addr)
			}
			if ln != nil || pc != nil {
				t.Fatal("a failed pair returned a socket")
			}
		})
	}
}

func TestListenPairError_ErrorAndUnwrap(t *testing.T) {
	cause := errors.New("bind refused")
	err := &ListenPairError{Network: NetworkUDP, Addr: "127.0.0.1:4433", Attempts: 3, Err: cause}
	if got, want := err.Error(), "netport: listen udp 127.0.0.1:4433 (attempt 3): bind refused"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, cause) {
		t.Fatal("Unwrap does not reach the bind failure")
	}
}

// fixedAddrListener reports addr as its address, standing in for a TCP
// listener bound at a number the test chose.
type fixedAddrListener struct {
	net.Listener
	addr net.Addr
}

// Addr implements net.Listener.
func (l *fixedAddrListener) Addr() net.Addr { return l.addr }

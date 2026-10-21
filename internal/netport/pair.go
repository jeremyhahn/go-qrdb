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
	"syscall"
)

// listen and listenPacket are the binds ListenPair makes, as variables so a
// test can stand in for the kernel and refuse a specific number.
var (
	listen       = net.Listen
	listenPacket = net.ListenPacket
)

// ListenPair binds a TCP listener at addr and a UDP socket on the same host
// and port, and returns both open: the listener family of a server that
// serves HTTP/2 on TCP and HTTP/3 on UDP under one advertised port.
//
// The UDP host and port are read back from the bound TCP listener rather than
// re-derived from addr, so the two sockets cannot disagree on what a bind
// string resolved to.
//
// WHEN addr NAMES PORT 0 THE PAIR IS REDRAWN, NOT JUST THE UDP HALF. The
// kernel draws the TCP port from its ephemeral range without looking at UDP,
// which is a separate namespace, so the same number can already be held on
// UDP by another process that asked for any port (go-qrdb#824: "listen udp
// 127.0.0.1:45373: bind: address already in use"). On that refusal the TCP
// listener is released and the kernel draws again, up to freePortAttempts
// times; nothing is released between the two binds of a pair, so a pair once
// returned is held. A named port is bound once: another number would not be
// the one the caller asked for.
//
// Failures are *ListenPairError, naming the network that refused.
func ListenPair(addr string) (net.Listener, net.PacketConn, error) {
	// A malformed addr leaves port empty and fails in the bind below, which
	// names the problem better than a parse error here would.
	_, port, _ := net.SplitHostPort(addr)
	attempts := 1
	if port == kernelDrawsPort {
		attempts = freePortAttempts
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		ln, err := listen(NetworkTCP, addr)
		if err != nil {
			return nil, nil, &ListenPairError{Network: NetworkTCP, Addr: addr, Attempts: attempt, Err: err}
		}
		udpAddr := ln.Addr().String()
		pc, err := listenPacket(NetworkUDP, udpAddr)
		if err == nil {
			return ln, pc, nil
		}
		_ = ln.Close()
		lastErr = &ListenPairError{Network: NetworkUDP, Addr: udpAddr, Attempts: attempt, Err: err}
		// Only a number another holder has is worth redrawing; any other
		// refusal would recur on every draw.
		if !errors.Is(err, syscall.EADDRINUSE) {
			break
		}
	}
	return nil, nil, lastErr
}

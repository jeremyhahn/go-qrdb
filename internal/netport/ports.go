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

// Package netport is the one place go-qrdb proves a listen port free before a
// listener binds it. It offers two pickers over a single probe-then-bind core:
//
//   - FreePortOn asks the kernel for a port, for qrdbd's auto-selected
//     listener ports in production.
//   - Reserve draws ports from a process-wide window below the kernel's
//     ephemeral range and locks each one across processes, for tests that
//     start many servers at once (see window.go for why port 0 is not enough
//     there).
//
// Both prove a port free AT THE HOST THE LISTENER WILL BIND AT and ON EVERY
// PROTOCOL its listener family will occupy, holding every probe socket until
// the whole batch is decided.
package netport

import (
	"io"
	"net"
	"strconv"
)

// NetworkTCP and NetworkUDP name the two port namespaces a listener can
// occupy. They are separate namespaces: a number free in one says nothing
// about the same number in the other.
const (
	NetworkTCP = "tcp"
	NetworkUDP = "udp"
)

// freePortAttempts bounds a FreePortOn search. A candidate is rejected only
// when another holder already has the number on one of the requested networks,
// so a handful of draws is ample; the bound exists so a host with a
// pathologically busy port space fails with a named error instead of spinning.
const freePortAttempts = 20

// kernelDrawsPort is the candidate that asks the kernel to pick a free port
// rather than naming one.
const kernelDrawsPort = "0"

// probe holds a candidate port open on one network and reports the address
// the kernel actually bound. Returning the address rather than just a closer
// lets the first probe of a batch report what ":0" resolved to, which the
// remaining probes then bind verbatim.
type probe func(addr string) (net.Addr, io.Closer, error)

// probes maps a network name to the way a port is held on it. Map-based
// dispatch rather than a switch, so an unknown network is a table miss that
// fails with a named error and a new namespace is one entry.
var probes = map[string]probe{
	NetworkTCP: func(addr string) (net.Addr, io.Closer, error) {
		ln, err := net.Listen(NetworkTCP, addr)
		if err != nil {
			return nil, nil, err
		}
		return ln.Addr(), ln, nil
	},
	NetworkUDP: func(addr string) (net.Addr, io.Closer, error) {
		pc, err := net.ListenPacket(NetworkUDP, addr)
		if err != nil {
			return nil, nil, err
		}
		return pc.LocalAddr(), pc, nil
	},
}

// FreePortOn returns a kernel-drawn port number that is free at host on every
// network in networks.
//
// THE PROBE BINDS AT THE HOST THE LISTENER WILL BIND AT, AND ON EVERY
// PROTOCOL THAT LISTENER FAMILY WILL OCCUPY. Both halves are load-bearing:
//
//   - Protocol. TCP and UDP are separate namespaces. The admin port is taken
//     on TCP by the REST listener and on UDP by its HTTP/3 sibling
//     (pkg/api/rest/server.go), so a TCP-only probe certifies a number another
//     process may already hold on UDP, and the daemon then fails to start with
//     "listen udp <host>:<port>: bind: address already in use" (go-qrdb#257).
//   - Scope. A port proven free on 127.0.0.1 says nothing about the same port
//     at wildcard scope: another process can hold it on a non-loopback local
//     address while loopback stays free. qrdbd binds its auto-selected ports
//     at server.host, whose default is "0.0.0.0" (go-qrdb#253, go-qrdb#248).
//
// A residual TOCTOU window remains between the last probe closing and the
// listener binding, and no version of this removes it. What it removes is the
// long-lived holder: a process that has held the number for hours on a
// protocol or an address the probe never looked at.
func FreePortOn(host string, networks ...string) (int, error) {
	return freePortOn(host, freePortAttempts, kernelDrawsPort, networks...)
}

// freePortOn is FreePortOn with its attempt budget and candidate exposed, so a
// test can ask whether one specific number is free on every network.
func freePortOn(host string, attempts int, candidate string, networks ...string) (int, error) {
	resolved, err := resolveProbes(host, networks)
	if err != nil {
		return 0, err
	}
	var lastErr error
	for range attempts {
		port, held, err := holdPort(host, candidate, resolved)
		if err == nil {
			_ = held.Close()
			return port, nil
		}
		lastErr = err
	}
	return 0, &NoFreePortError{Host: host, Networks: networks, Attempts: attempts, Err: lastErr}
}

// resolveProbes looks every network up in the probe table once, before any
// binding. An unknown network is a coding error rather than a busy host, and
// retrying it would report a transient-looking failure for a permanent cause.
func resolveProbes(host string, networks []string) ([]probe, error) {
	if len(networks) == 0 {
		return nil, &NoProbeNetworkError{Host: host}
	}
	resolved := make([]probe, len(networks))
	for i, network := range networks {
		p, ok := probes[network]
		if !ok {
			return nil, &UnknownProbeNetworkError{Network: network}
		}
		resolved[i] = p
	}
	return resolved, nil
}

// holdPort takes one candidate port at host on every probe and returns the
// port it proved together with a closer releasing every socket it holds.
//
// candidate is kernelDrawsPort to let the first probe ask the kernel for any
// free number; naming a specific port instead asks whether THAT number is free
// on every probe.
//
// EVERY SOCKET STAYS OPEN UNTIL THE CALLER CLOSES THE RESULT. Releasing the
// first probe before opening the second would let another process take the
// number in between, and the function would then certify a port it had itself
// just given away. On failure everything already held is released.
func holdPort(host, candidate string, resolved []probe) (int, io.Closer, error) {
	held := make(closers, 0, len(resolved))
	// The first probe resolves the candidate; every later probe must bind the
	// exact number the first one ended up with.
	port := candidate
	for _, p := range resolved {
		bound, closer, err := p(net.JoinHostPort(host, port))
		if err != nil {
			_ = held.Close()
			return 0, nil, err
		}
		held = append(held, closer)
		if port, err = portOfAddr(bound); err != nil {
			_ = held.Close()
			return 0, nil, err
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		_ = held.Close()
		return 0, nil, &ProbeAddrError{Addr: port, Err: err}
	}
	return n, held, nil
}

// portOfAddr returns the port component of a bound listener address.
//
// It reads the port off net.Addr's own String form rather than type-asserting
// to *net.TCPAddr / *net.UDPAddr, so one spelling serves both namespaces and
// no assertion can panic on a type the probe table later grows.
func portOfAddr(addr net.Addr) (string, error) {
	_, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "", &ProbeAddrError{Addr: addr.String(), Err: err}
	}
	return port, nil
}

// closers releases a set of sockets or locks together.
type closers []io.Closer

// Close closes every member, ignoring individual failures: a probe or lock
// that fails to close has nothing further to release.
func (c closers) Close() error {
	for _, closer := range c {
		_ = closer.Close()
	}
	return nil
}

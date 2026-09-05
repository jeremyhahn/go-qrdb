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
	"io"
	"net"
	"strconv"
)

// portAttempts bounds every port search in this file. A candidate is rejected
// only when another holder already has the number on one of the requested
// networks, so a handful of draws is ample; the bound exists so a host with a
// pathologically busy port space fails with a named error instead of spinning.
const portAttempts = 20

// networkTCP and networkUDP name the two port namespaces a listener can
// occupy. They are separate namespaces: a number free in one says nothing
// about the same number in the other.
const (
	networkTCP = "tcp"
	networkUDP = "udp"
)

// portProbe holds a candidate port open on one network and reports the address
// the kernel actually bound. Returning the address rather than just a closer
// lets the first probe of a batch report what ":0" resolved to, which the
// remaining probes then bind verbatim.
type portProbe func(addr string) (net.Addr, io.Closer, error)

// portProbes maps a network name to the way a port is held on it. Map-based
// dispatch rather than a switch, so an unknown network is a table miss that
// fails with a named error and a new namespace is one entry.
var portProbes = map[string]portProbe{
	networkTCP: func(addr string) (net.Addr, io.Closer, error) {
		ln, err := net.Listen(networkTCP, addr)
		if err != nil {
			return nil, nil, err
		}
		return ln.Addr(), ln, nil
	},
	networkUDP: func(addr string) (net.Addr, io.Closer, error) {
		pc, err := net.ListenPacket(networkUDP, addr)
		if err != nil {
			return nil, nil, err
		}
		return pc.LocalAddr(), pc, nil
	},
}

// freePortOn returns a port number that is free at host on every network in
// networks.
//
// THE PROBE BINDS AT THE HOST THE LISTENER WILL BIND AT, AND ON EVERY
// PROTOCOL THAT LISTENER FAMILY WILL OCCUPY. Both halves are load-bearing, and
// the helpers this replaces violated both:
//
//   - Protocol. TCP and UDP are separate namespaces. The admin port is taken
//     on TCP by the REST listener and on UDP by its HTTP/3 sibling
//     (pkg/api/rest/server.go), so a TCP-only probe certifies a number another
//     process may already hold on UDP, and the daemon then fails to start with
//     "listen udp <host>:<port>: bind: address already in use" (go-qrdb#257).
//   - Scope. A port proven free on 127.0.0.1 says nothing about the same port
//     at wildcard scope: another process can hold it on a non-loopback local
//     address while loopback stays free. Every auto-selected port here is
//     bound at server.host, whose default is "0.0.0.0", while the replaced
//     probes were hardcoded to loopback. That is go-qrdb#253, which the #248
//     review reached one axis over from #248's own thesis.
//
// A residual TOCTOU window remains between the last probe closing and the
// daemon binding, and no version of this removes it. What it removes is the
// long-lived holder: a process that has held the number for hours on a
// protocol or an address the probe never looked at.
func freePortOn(host string, networks ...string) (int, error) {
	// Validate the table lookups once, before any binding. An unknown network
	// is a coding error rather than a busy host, and retrying it 20 times
	// would report a transient-looking failure for a permanent cause.
	if len(networks) == 0 {
		return 0, &NoProbeNetworkError{Host: host}
	}
	probes := make([]portProbe, len(networks))
	for i, network := range networks {
		probe, ok := portProbes[network]
		if !ok {
			return 0, &UnknownProbeNetworkError{Network: network}
		}
		probes[i] = probe
	}

	var lastErr error
	for i := 0; i < portAttempts; i++ {
		// "0" asks the kernel to draw the candidate. A caller that already
		// has a number in mind passes it instead; see reservePortOn.
		port, err := reservePortOn(host, kernelDrawsPort, probes)
		if err == nil {
			return port, nil
		}
		lastErr = err
	}
	return 0, &NoFreePortError{Host: host, Networks: networks, Attempts: portAttempts, Err: lastErr}
}

// kernelDrawsPort is the candidate that asks the kernel to pick a free port
// rather than naming one.
const kernelDrawsPort = "0"

// reservePortOn takes one candidate port at host and proves it free on every
// probe, returning the port it proved.
//
// candidate is normally kernelDrawsPort, which lets the first probe ask the
// kernel for any free number; naming a specific port instead asks whether THAT
// number is free on every probe, which is how the acceptance rule itself is
// tested against a port whose two protocols disagree.
//
// EVERY SOCKET STAYS OPEN UNTIL THE WHOLE BATCH SUCCEEDS. Releasing the first
// probe before opening the second would let another process take the number in
// between, and the function would then certify a port it had itself just given
// away — a check answering about a state that no longer holds when it answers.
func reservePortOn(host, candidate string, probes []portProbe) (int, error) {
	var held []io.Closer
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()

	// The first probe resolves the candidate; every later probe must bind
	// the exact number the first one ended up with.
	port := candidate
	for _, probe := range probes {
		bound, closer, err := probe(net.JoinHostPort(host, port))
		if err != nil {
			return 0, err
		}
		held = append(held, closer)
		if port, err = portOfAddr(bound); err != nil {
			return 0, err
		}
	}
	return strconv.Atoi(port)
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

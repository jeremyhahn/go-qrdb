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

package qrdbdharness

import "github.com/jeremyhahn/go-qrdb/internal/netport"

// loopbackHost is the address every harness daemon listens on, and so the
// address its ports are proven free at.
const loopbackHost = "127.0.0.1"

// ReserveTCPPort reserves a single free TCP port. See ReserveTCPPorts.
func ReserveTCPPort() (int, error) {
	ports, err := ReserveTCPPorts(1)
	if err != nil {
		return 0, err
	}
	return ports[0], nil
}

// ReserveTCPPorts reserves n free loopback ports for server listen addresses
// through netport.Reserve: drawn from a window below the kernel's ephemeral
// range, bind-verified on TCP and on UDP with every probe held until the whole
// batch is decided, and never handed out twice by this process or by any other
// in the same network namespace. Because every port is certified on both
// protocols, a REST admin port -- whose HTTP/3 sibling binds the same number on
// UDP -- is safe to take from here. Failures are netport's typed errors.
func ReserveTCPPorts(n int) ([]int, error) {
	return netport.Reserve(loopbackHost, n)
}

// ReserveUDPPort reserves a single free UDP port. See ReserveUDPPorts.
func ReserveUDPPort() (int, error) {
	ports, err := ReserveUDPPorts(1)
	if err != nil {
		return 0, err
	}
	return ports[0], nil
}

// ReserveUDPPorts reserves n free loopback ports for Raft/QUIC listen
// addresses. It draws from the same reservation as ReserveTCPPorts, so a
// number handed out for one protocol is never handed out for the other.
func ReserveUDPPorts(n int) ([]int, error) {
	return netport.Reserve(loopbackHost, n)
}

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

import "testing"

// The reservation itself -- the window, the cross-process lock, the
// dual-protocol probe -- is tested in internal/netport. These tests pin what
// the harness adds: one shared reservation behind both protocol spellings.

func TestReservePortBatchUniqueness(t *testing.T) {
	tcp, err := ReserveTCPPorts(2)
	if err != nil {
		t.Fatalf("ReserveTCPPorts: %v", err)
	}
	udp, err := ReserveUDPPorts(2)
	if err != nil {
		t.Fatalf("ReserveUDPPorts: %v", err)
	}
	seen := map[int]struct{}{}
	for _, port := range append(tcp, udp...) {
		if _, ok := seen[port]; ok {
			t.Fatalf("port %d was reserved twice", port)
		}
		seen[port] = struct{}{}
	}
}

func TestReserveSinglePortsAreDistinct(t *testing.T) {
	tcp, err := ReserveTCPPort()
	if err != nil {
		t.Fatalf("ReserveTCPPort: %v", err)
	}
	udp, err := ReserveUDPPort()
	if err != nil {
		t.Fatalf("ReserveUDPPort: %v", err)
	}
	if tcp <= 0 || udp <= 0 || tcp == udp {
		t.Fatalf("ReserveTCPPort = %d, ReserveUDPPort = %d; want two distinct ports", tcp, udp)
	}
}

func TestReservePortsOfZeroReservesNothing(t *testing.T) {
	for name, reserve := range map[string]func(int) ([]int, error){
		"TCP": ReserveTCPPorts,
		"UDP": ReserveUDPPorts,
	} {
		ports, err := reserve(0)
		if err != nil || len(ports) != 0 {
			t.Errorf("Reserve%sPorts(0) = (%v, %v), want no ports and no error", name, ports, err)
		}
	}
}

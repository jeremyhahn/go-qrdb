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
	"testing"
)

func TestErrorMessages(t *testing.T) {
	cause := errors.New("cause")
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "NoProbeNetworkError",
			err:  &NoProbeNetworkError{Host: "0.0.0.0"},
			want: `netport: port search at "0.0.0.0" named no network; a port must be proven free on the protocols its listener will bind`,
		},
		{
			name: "UnknownProbeNetworkError",
			err:  &UnknownProbeNetworkError{Network: "sctp"},
			want: `netport: unknown port-probe network "sctp" (want "tcp" or "udp")`,
		},
		{
			name: "ProbeAddrError",
			err:  &ProbeAddrError{Addr: "no-port", Err: cause},
			want: `netport: cannot read the port from probe address "no-port": cause`,
		},
		{
			name: "NoFreePortError with cause",
			err:  &NoFreePortError{Host: "::1", Networks: []string{NetworkTCP, NetworkUDP}, Attempts: 20, Err: cause},
			want: `netport: no port free on tcp+udp at "::1" after 20 attempts: last bind error: cause`,
		},
		{
			name: "NoFreePortError without cause",
			err:  &NoFreePortError{Host: "::1", Networks: []string{NetworkTCP}, Attempts: 0},
			want: `netport: no port free on tcp at "::1" after 0 attempts`,
		},
		{
			name: "EphemeralRangeError with cause",
			err:  &EphemeralRangeError{Path: "/range", Reason: "read failed", Err: cause},
			want: "netport: cannot determine the kernel ephemeral port range from /range: read failed: cause",
		},
		{
			name: "EphemeralRangeError without cause",
			err:  &EphemeralRangeError{Path: "/range", Reason: "expected two fields, got 1"},
			want: "netport: cannot determine the kernel ephemeral port range from /range: expected two fields, got 1",
		},
		{
			name: "PortWindowError",
			err:  &PortWindowError{EphemeralLow: 2048, Window: 8192},
			want: "netport: ephemeral floor 2048 leaves no room for a 8192-port reservation window",
		},
		{
			name: "PortExhaustedError",
			err:  &PortExhaustedError{Window: 8192, Base: 24576, Low: 32768},
			want: "netport: exhausted the 8192-port reservation window [24576,32768)",
		},
		{
			name: "PortReservationError with cause",
			err:  &PortReservationError{Host: "127.0.0.1", Networks: []string{NetworkTCP, NetworkUDP}, Requested: 3, Reserved: 1, LastErr: cause},
			want: `netport: could not reserve 3 unique ports free on tcp+udp at "127.0.0.1" (reserved 1): last error: cause`,
		},
		{
			name: "PortReservationError without cause",
			err:  &PortReservationError{Host: "127.0.0.1", Networks: []string{NetworkUDP}, Requested: 2},
			want: `netport: could not reserve 2 unique ports free on udp at "127.0.0.1" (reserved 0)`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestErrorUnwrap(t *testing.T) {
	cause := errors.New("cause")
	wrapping := []error{
		&ProbeAddrError{Addr: "x", Err: cause},
		&NoFreePortError{Err: cause},
		&EphemeralRangeError{Err: cause},
		&PortReservationError{LastErr: cause},
	}
	for _, err := range wrapping {
		if !errors.Is(err, cause) {
			t.Errorf("%T does not unwrap to its cause", err)
		}
	}
	bare := []error{
		&NoFreePortError{},
		&EphemeralRangeError{},
		&PortReservationError{},
	}
	for _, err := range bare {
		if errors.Unwrap(err) != nil {
			t.Errorf("%T without a cause unwraps to %v", err, errors.Unwrap(err))
		}
	}
}

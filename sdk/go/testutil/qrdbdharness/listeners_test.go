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

import (
	"errors"
	"slices"
	"strconv"
	"testing"
)

// appendedPorts parses the flag/port pairs ReserveListenerPorts appended
// after the first n args.
func appendedPorts(t *testing.T, out []string, n int) map[string]int {
	t.Helper()
	tail := out[n:]
	if len(tail)%2 != 0 {
		t.Fatalf("appended args %q are not flag/value pairs", tail)
	}
	ports := make(map[string]int, len(tail)/2)
	for i := 0; i < len(tail); i += 2 {
		port, err := strconv.Atoi(tail[i+1])
		if err != nil {
			t.Fatalf("%s value %q is not a port: %v", tail[i], tail[i+1], err)
		}
		if _, dup := ports[tail[i]]; dup {
			t.Fatalf("%s appended twice: %q", tail[i], tail)
		}
		ports[tail[i]] = port
	}
	return ports
}

func TestReserveListenerPortsFillsDefaultOnListeners(t *testing.T) {
	low, err := readEphemeralLow(ephemeralRangePath)
	if err != nil {
		t.Fatalf("readEphemeralLow: %v", err)
	}
	args := []string{"start", "--data-dir", "/data"}

	out, err := ReserveListenerPorts(args)
	if err != nil {
		t.Fatalf("ReserveListenerPorts: %v", err)
	}
	if !slices.Equal(out[:len(args)], args) {
		t.Fatalf("leading args = %q, want %q unchanged", out[:len(args)], args)
	}
	ports := appendedPorts(t, out, len(args))
	want := []string{"--admin-port", "--grpc-port", "--bootstrap-noise-port"}
	if len(ports) != len(want) {
		t.Fatalf("appended %v, want exactly %q", ports, want)
	}
	seen := make(map[int]bool, len(ports))
	for _, flag := range want {
		port, ok := ports[flag]
		if !ok {
			t.Fatalf("%s not appended: %q", flag, out)
		}
		if port < low-portWindow || port >= low {
			t.Errorf("%s = %d, outside the reservation window [%d, %d)", flag, port, low-portWindow, low)
		}
		if seen[port] {
			t.Errorf("port %d handed to two listeners", port)
		}
		seen[port] = true
	}
}

func TestReserveListenerPortsReservesEnabledListeners(t *testing.T) {
	args := []string{"start", "--enable-quic", "--enable-mcp", "--cluster-join-enabled",
		"--enable-mysql", "--enable-pgsql=true"}

	out, err := ReserveListenerPorts(args)
	if err != nil {
		t.Fatalf("ReserveListenerPorts: %v", err)
	}
	ports := appendedPorts(t, out, len(args))
	for _, l := range defaultPortListeners {
		if _, ok := ports[l.port]; !ok {
			t.Errorf("%s not appended: %q", l.port, out)
		}
	}
}

func TestReserveListenerPortsKeepsChosenPorts(t *testing.T) {
	args := []string{"start", "--admin-port", "1234", "--grpc-port=5678",
		"--enable-quic", "--quic-port", "4321", "--bootstrap-noise-port", "8765"}

	out, err := ReserveListenerPorts(args)
	if err != nil {
		t.Fatalf("ReserveListenerPorts: %v", err)
	}
	if !slices.Equal(out, args) {
		t.Fatalf("args = %q, want %q: every listener's port was already chosen", out, args)
	}
}

func TestReserveListenerPortsLeavesOtherCommands(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"status"}, {"version", "--admin-port"}} {
		out, err := ReserveListenerPorts(args)
		if err != nil {
			t.Fatalf("ReserveListenerPorts(%q): %v", args, err)
		}
		if !slices.Equal(out, args) {
			t.Errorf("ReserveListenerPorts(%q) = %q, want it unchanged", args, out)
		}
	}
}

func TestReserveListenerPortsDoesNotWriteThroughInput(t *testing.T) {
	backing := make([]string, 1, 16)
	backing[0] = "start"

	out, err := ReserveListenerPorts(backing)
	if err != nil {
		t.Fatalf("ReserveListenerPorts: %v", err)
	}
	if len(out) == len(backing) {
		t.Fatalf("nothing appended: %q", out)
	}
	if spare := backing[1:cap(backing)]; slices.ContainsFunc(spare, func(s string) bool { return s != "" }) {
		t.Fatalf("caller's backing array was written: %q", spare)
	}
}

func TestReserveListenerPortsPropagatesReservationError(t *testing.T) {
	want := &PortReservationError{Network: "TCP", Requested: 1}
	saved := defaultPortListeners
	t.Cleanup(func() { defaultPortListeners = saved })
	defaultPortListeners = []listenerFlag{
		{port: "--admin-port", reserve: func() (int, error) { return 0, want }},
	}

	out, err := ReserveListenerPorts([]string{"start"})
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if out != nil {
		t.Fatalf("args = %q, want nil on error", out)
	}
}

func TestHasFlag(t *testing.T) {
	tests := []struct {
		args []string
		name string
		want bool
	}{
		{[]string{"start", "--admin-port", "1"}, "--admin-port", true},
		{[]string{"start", "--admin-port=1"}, "--admin-port", true},
		{[]string{"start", "--admin-portal", "1"}, "--admin-port", false},
		{[]string{"start", "--enable-quic"}, "--enable-quic", true},
		{[]string{"start"}, "--enable-quic", false},
		{nil, "--admin-port", false},
	}
	for _, tt := range tests {
		if got := hasFlag(tt.args, tt.name); got != tt.want {
			t.Errorf("hasFlag(%q, %q) = %v, want %v", tt.args, tt.name, got, tt.want)
		}
	}
}

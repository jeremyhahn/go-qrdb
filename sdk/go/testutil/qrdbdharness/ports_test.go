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
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

// fixedRangeFile writes an ip_local_port_range fixture and returns its path.
func fixedRangeFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ip_local_port_range")
	if err := os.WriteFile(path, []byte(contents), fileMode); err != nil {
		t.Fatalf("write range fixture: %v", err)
	}
	return path
}

// closerFunc adapts a function to io.Closer for listener stubs.
type closerFunc func() error

func (f closerFunc) Close() error { return f() }

func TestReadEphemeralLow(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		want     int
		reason   string
	}{
		{name: "kernel format", contents: "32768\t60999\n", want: 32768},
		{name: "space separated", contents: "15000 61000", want: 15000},
		{name: "single field", contents: "32768\n", reason: "expected two fields, got 1"},
		{name: "three fields", contents: "1 2 3", reason: "expected two fields, got 3"},
		{name: "not a number", contents: "low high", reason: "lower bound is not a number"},
		{name: "zero", contents: "0 60999", reason: "lower bound 0 is out of range"},
		{name: "negative", contents: "-1 60999", reason: "lower bound -1 is out of range"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			low, err := readEphemeralLow(fixedRangeFile(t, tc.contents))
			if tc.reason == "" {
				if err != nil {
					t.Fatalf("readEphemeralLow: %v", err)
				}
				if low != tc.want {
					t.Errorf("low = %d, want %d", low, tc.want)
				}
				return
			}
			var rangeErr *EphemeralRangeError
			if !errors.As(err, &rangeErr) {
				t.Fatalf("err = %v, want *EphemeralRangeError", err)
			}
			if rangeErr.Reason != tc.reason {
				t.Errorf("Reason = %q, want %q", rangeErr.Reason, tc.reason)
			}
		})
	}
}

func TestReadEphemeralLowMissingFile(t *testing.T) {
	_, err := readEphemeralLow(filepath.Join(t.TempDir(), "absent"))
	var rangeErr *EphemeralRangeError
	if !errors.As(err, &rangeErr) {
		t.Fatalf("err = %v, want *EphemeralRangeError", err)
	}
	if rangeErr.Reason != "read failed" {
		t.Errorf("Reason = %q, want read failed", rangeErr.Reason)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Error("error does not unwrap to os.ErrNotExist")
	}
}

func TestReadEphemeralLowFromKernel(t *testing.T) {
	low, err := readEphemeralLow(ephemeralRangePath)
	if err != nil {
		t.Fatalf("the harness requires a readable %s: %v", ephemeralRangePath, err)
	}
	if low <= lowestReservablePort {
		t.Fatalf("kernel ephemeral floor %d is too low for a %d-port window", low, portWindow)
	}
}

func TestAllocatorEphemeralLowIsReadOnce(t *testing.T) {
	path := fixedRangeFile(t, "32768 60999")
	a := newAllocator(path)

	low, err := a.ephemeralLow()
	if err != nil {
		t.Fatalf("ephemeralLow: %v", err)
	}
	if low != 32768 {
		t.Fatalf("low = %d, want 32768", low)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	again, err := a.ephemeralLow()
	if err != nil || again != low {
		t.Fatalf("ephemeralLow re-read the file: got (%d, %v)", again, err)
	}
}

func TestAllocatorNextPortDrawsBelowEphemeralRange(t *testing.T) {
	const low = 32768
	a := newAllocator(fixedRangeFile(t, "32768 60999"))

	first, err := a.nextPort()
	if err != nil {
		t.Fatalf("nextPort: %v", err)
	}
	if want := low - portWindow; first != want {
		t.Errorf("first candidate = %d, want the window base %d", first, want)
	}

	seen := map[int]struct{}{first: {}}
	for i := 0; i < 64; i++ {
		port, err := a.nextPort()
		if err != nil {
			t.Fatalf("nextPort: %v", err)
		}
		if port >= low {
			t.Fatalf("candidate %d is inside the kernel ephemeral range (>= %d)", port, low)
		}
		if port < low-portWindow {
			t.Fatalf("candidate %d is below the window base %d", port, low-portWindow)
		}
		if _, dup := seen[port]; dup {
			t.Fatalf("candidate %d handed out twice", port)
		}
		seen[port] = struct{}{}
	}
}

func TestAllocatorNextPortSkipsAlreadyReserved(t *testing.T) {
	a := newAllocator(fixedRangeFile(t, "32768 60999"))
	base := 32768 - portWindow
	a.reserved[base] = struct{}{}
	a.reserved[base+1] = struct{}{}

	port, err := a.nextPort()
	if err != nil {
		t.Fatalf("nextPort: %v", err)
	}
	if port != base+2 {
		t.Errorf("port = %d, want %d (the first unreserved candidate)", port, base+2)
	}
}

func TestAllocatorNextPortWindowTooLow(t *testing.T) {
	a := newAllocator(fixedRangeFile(t, strconv.Itoa(lowestReservablePort+portWindow-1)+" 60999"))
	_, err := a.nextPort()
	var windowErr *PortWindowError
	if !errors.As(err, &windowErr) {
		t.Fatalf("err = %v, want *PortWindowError", err)
	}
	if windowErr.Window != portWindow {
		t.Errorf("Window = %d, want %d", windowErr.Window, portWindow)
	}
}

func TestAllocatorNextPortExhausted(t *testing.T) {
	const low = 32768
	a := newAllocator(fixedRangeFile(t, "32768 60999"))
	a.next = low // the window has already been walked end to end

	_, err := a.nextPort()
	var exhaustedErr *PortExhaustedError
	if !errors.As(err, &exhaustedErr) {
		t.Fatalf("err = %v, want *PortExhaustedError", err)
	}
	if exhaustedErr.Low != low || exhaustedErr.Base != low-portWindow {
		t.Errorf("window = [%d,%d), want [%d,%d)", exhaustedErr.Base, exhaustedErr.Low, low-portWindow, low)
	}
}

func TestAllocatorNextPortPropagatesRangeError(t *testing.T) {
	a := newAllocator(filepath.Join(t.TempDir(), "absent"))
	if _, err := a.nextPort(); err == nil {
		t.Fatal("nextPort succeeded without a readable ephemeral range")
	}
}

func TestAllocatorReserveBindVerifies(t *testing.T) {
	a := newAllocator(fixedRangeFile(t, "32768 60999"))
	var probed []string
	ports, err := a.reserve("TCP", 3, func(addr string) (io.Closer, error) {
		probed = append(probed, addr)
		return closerFunc(func() error { return nil }), nil
	})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if len(ports) != 3 {
		t.Fatalf("ports = %v, want 3 of them", ports)
	}
	if len(probed) != 3 {
		t.Fatalf("probed %v, want one bind per port", probed)
	}
	for i, addr := range probed {
		host, port, splitErr := net.SplitHostPort(addr)
		if splitErr != nil {
			t.Fatalf("SplitHostPort(%q): %v", addr, splitErr)
		}
		if host != loopbackHost {
			t.Errorf("probe host = %q, want %q", host, loopbackHost)
		}
		if port != strconv.Itoa(ports[i]) {
			t.Errorf("probe port = %q, want %d", port, ports[i])
		}
	}
}

func TestAllocatorReserveHoldsProbesUntilBatchComplete(t *testing.T) {
	a := newAllocator(fixedRangeFile(t, "32768 60999"))
	var mu sync.Mutex
	open := 0
	maxOpen := 0
	if _, err := a.reserve("TCP", 4, func(string) (io.Closer, error) {
		mu.Lock()
		open++
		if open > maxOpen {
			maxOpen = open
		}
		mu.Unlock()
		return closerFunc(func() error {
			mu.Lock()
			open--
			mu.Unlock()
			return nil
		}), nil
	}); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if maxOpen != 4 {
		t.Errorf("peak open probes = %d, want 4 (probes must be held until the batch completes)", maxOpen)
	}
	if open != 0 {
		t.Errorf("%d probe listeners left open", open)
	}
}

func TestAllocatorReserveSkipsBusyPorts(t *testing.T) {
	a := newAllocator(fixedRangeFile(t, "32768 60999"))
	base := 32768 - portWindow
	ports, err := a.reserve("TCP", 1, func(addr string) (io.Closer, error) {
		_, port, _ := net.SplitHostPort(addr)
		if port == strconv.Itoa(base) {
			return nil, errors.New("address already in use")
		}
		return closerFunc(func() error { return nil }), nil
	})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if len(ports) != 1 || ports[0] != base+1 {
		t.Errorf("ports = %v, want [%d] after skipping the busy candidate", ports, base+1)
	}
}

func TestAllocatorReserveFailsWhenNothingBinds(t *testing.T) {
	a := newAllocator(fixedRangeFile(t, "32768 60999"))
	bindErr := errors.New("address already in use")
	_, err := a.reserve("UDP", 2, func(string) (io.Closer, error) { return nil, bindErr })
	var reservationErr *PortReservationError
	if !errors.As(err, &reservationErr) {
		t.Fatalf("err = %v, want *PortReservationError", err)
	}
	if reservationErr.Requested != 2 || reservationErr.Reserved != 0 || reservationErr.Network != "UDP" {
		t.Errorf("unexpected error detail: %+v", reservationErr)
	}
	if !errors.Is(err, bindErr) {
		t.Error("error does not unwrap to the last bind failure")
	}
}

func TestAllocatorReserveZeroPorts(t *testing.T) {
	a := newAllocator(fixedRangeFile(t, "32768 60999"))
	ports, err := a.reserve("TCP", 0, func(string) (io.Closer, error) {
		t.Fatal("reserve(0) must not probe any port")
		return nil, nil
	})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if len(ports) != 0 {
		t.Errorf("ports = %v, want none", ports)
	}
}

func TestAllocatorReservePropagatesWindowError(t *testing.T) {
	a := newAllocator(fixedRangeFile(t, "1024 60999"))
	_, err := a.reserve("TCP", 1, func(string) (io.Closer, error) {
		t.Fatal("reserve must not probe when the window is unusable")
		return nil, nil
	})
	var windowErr *PortWindowError
	if !errors.As(err, &windowErr) {
		t.Fatalf("err = %v, want *PortWindowError", err)
	}
}

func TestReservePortsAreUsableAndUnique(t *testing.T) {
	low, err := readEphemeralLow(ephemeralRangePath)
	if err != nil {
		t.Fatalf("readEphemeralLow: %v", err)
	}

	tcp, err := ReserveTCPPorts(2)
	if err != nil {
		t.Fatalf("ReserveTCPPorts: %v", err)
	}
	udp, err := ReserveUDPPorts(2)
	if err != nil {
		t.Fatalf("ReserveUDPPorts: %v", err)
	}
	single, err := ReserveTCPPort()
	if err != nil {
		t.Fatalf("ReserveTCPPort: %v", err)
	}
	singleUDP, err := ReserveUDPPort()
	if err != nil {
		t.Fatalf("ReserveUDPPort: %v", err)
	}

	all := append(append(append(append([]int{}, tcp...), udp...), single), singleUDP)
	seen := make(map[int]struct{}, len(all))
	for _, port := range all {
		if port >= low {
			t.Errorf("port %d is inside the kernel ephemeral range (>= %d)", port, low)
		}
		if port < low-portWindow {
			t.Errorf("port %d is below the window base %d", port, low-portWindow)
		}
		if _, dup := seen[port]; dup {
			t.Errorf("port %d was handed out twice, including across TCP and UDP", port)
		}
		seen[port] = struct{}{}
	}

	// A reserved port must actually be bindable once the probe is released.
	ln, err := net.Listen("tcp", net.JoinHostPort(loopbackHost, strconv.Itoa(single)))
	if err != nil {
		t.Fatalf("reserved TCP port %d is not bindable: %v", single, err)
	}
	if err := ln.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	pc, err := net.ListenPacket("udp", net.JoinHostPort(loopbackHost, strconv.Itoa(singleUDP)))
	if err != nil {
		t.Fatalf("reserved UDP port %d is not bindable: %v", singleUDP, err)
	}
	if err := pc.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestReservePortsConcurrently(t *testing.T) {
	const goroutines = 8
	const perGoroutine = 4

	var wg sync.WaitGroup
	results := make([][]int, goroutines)
	errs := make([]error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = ReserveTCPPorts(perGoroutine)
		}(i)
	}
	wg.Wait()

	seen := make(map[int]struct{}, goroutines*perGoroutine)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
		for _, port := range results[i] {
			if _, dup := seen[port]; dup {
				t.Errorf("port %d handed to two concurrent callers", port)
			}
			seen[port] = struct{}{}
		}
	}
}

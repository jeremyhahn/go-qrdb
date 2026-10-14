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
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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

// unlocked is a portLocker that always grants the lock. Fixture allocators use
// it so their exact candidate sequences do not depend on which real ports the
// default allocator -- or another test process -- has locked.
func unlocked(int) (io.Closer, error) { return closerFunc(func() error { return nil }), nil }

// reservePortsHelperMode turns this test binary into a second harness process
// that reserves reservePortsHelperCount UDP ports and prints them.
const (
	reservePortsHelperMode  = "reserve-udp"
	reservePortsHelperCount = 4
)

// TestReservePortsAreDisjointAcrossProcesses is the cross-process race `go
// test` hits by running packages concurrently. Every harness process walks
// the same window from the same base; before the cross-process lock, a second
// process bind-verified the ports this one had already handed out (their
// probes long closed) and handed them out again.
func TestReservePortsAreDisjointAcrossProcesses(t *testing.T) {
	if _, err := ReserveUDPPorts(reservePortsHelperCount); err != nil {
		t.Fatalf("ReserveUDPPorts: %v", err)
	}

	// #nosec G204,G702 -- re-exec of this test binary: argv[0] is os.Args[0], the path `go test` invoked, with no arguments; the helper mode travels in a compile-time-literal environment entry. No shell is involved.
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), helperEnv+"="+reservePortsHelperMode)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("second harness process: %v: %s", err, stderr.String())
	}
	child := strings.Fields(string(out))
	if len(child) != reservePortsHelperCount {
		t.Fatalf("second process reserved %v, want %d ports", child, reservePortsHelperCount)
	}

	defaultAllocator.mu.Lock()
	defer defaultAllocator.mu.Unlock()
	for _, s := range child {
		port, err := strconv.Atoi(s)
		if err != nil {
			t.Fatalf("second process printed %q: %v", s, err)
		}
		if _, ours := defaultAllocator.reserved[port]; ours {
			t.Errorf("port %d was handed out by this process AND by a concurrent one", port)
		}
	}
}

// probeSockets records the sockets probeWith opens, so a test can ask those
// sockets whether they were closed instead of re-binding their number: between
// a release and a re-bind, any other process on the host may take the number.
type probeSockets struct {
	tcpAddr func(addr string) string
	tcp     net.Listener
	udp     net.PacketConn
}

// listenTCP binds TCP at p.tcpAddr(addr), or at addr when tcpAddr is nil.
func (p *probeSockets) listenTCP(network, addr string) (net.Listener, error) {
	if p.tcpAddr != nil {
		addr = p.tcpAddr(addr)
	}
	ln, err := net.Listen(network, addr)
	p.tcp = ln
	return ln, err
}

// listenUDP binds UDP at addr.
func (p *probeSockets) listenUDP(network, addr string) (net.PacketConn, error) {
	pc, err := net.ListenPacket(network, addr)
	p.udp = pc
	return pc, err
}

// assertClosed fails t unless c was already closed. A second Close on a
// closed socket reports net.ErrClosed; on an open one it closes it, so a leak
// fails here without outliving the test.
func assertClosed(t *testing.T, what string, c io.Closer) {
	t.Helper()
	if c == nil {
		t.Fatalf("the probe never opened its %s socket", what)
	}
	if err := c.Close(); !errors.Is(err, net.ErrClosed) {
		t.Errorf("the probe left its %s socket open (second Close: %v)", what, err)
	}
}

// TestProbeBothProtocolsRefusesAPortHeldOnUDP: a number free on TCP but held
// on UDP is not free for a REST listener, whose HTTP/3 sibling binds the same
// number on UDP. A TCP-only probe certified it (go-qrdb#248). The refused
// probe must close the TCP listener it opened first.
func TestProbeBothProtocolsRefusesAPortHeldOnUDP(t *testing.T) {
	holder, err := net.ListenPacket("udp", net.JoinHostPort(loopbackHost, "0"))
	if err != nil {
		t.Fatalf("hold a UDP port: %v", err)
	}
	defer func() { _ = holder.Close() }()
	addr := holder.LocalAddr().String()

	// The held number is in the kernel's ephemeral range, where any process's
	// outbound connection may already own it on TCP; the TCP side therefore
	// binds a kernel-chosen number, so only the UDP refusal is under test.
	sockets := &probeSockets{tcpAddr: func(string) string { return net.JoinHostPort(loopbackHost, "0") }}
	if probe, err := probeWith(sockets.listenTCP, sockets.listenUDP, addr); err == nil {
		_ = probe.Close()
		t.Fatalf("probe certified %s, which is held on UDP", addr)
	}
	if sockets.udp != nil {
		t.Fatalf("the probe bound %s on UDP while it was held", addr)
	}
	assertClosed(t, "TCP", sockets.tcp)
}

// TestProbeBothProtocolsHoldsBothUntilClosed: a certified port is held on both
// protocols while the batch is decided, and released on both by Close.
func TestProbeBothProtocolsHoldsBothUntilClosed(t *testing.T) {
	port, err := ReserveTCPPort()
	if err != nil {
		t.Fatalf("ReserveTCPPort: %v", err)
	}
	addr := net.JoinHostPort(loopbackHost, strconv.Itoa(port))
	sockets := &probeSockets{}
	probe, err := probeWith(sockets.listenTCP, sockets.listenUDP, addr)
	if err != nil {
		t.Fatalf("probe %s: %v", addr, err)
	}
	if ln, err := net.Listen("tcp", addr); err == nil {
		_ = ln.Close()
		t.Fatal("the probe does not hold TCP")
	}
	if pc, err := net.ListenPacket("udp", addr); err == nil {
		_ = pc.Close()
		t.Fatal("the probe does not hold UDP")
	}
	if err := probe.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertClosed(t, "TCP", sockets.tcp)
	assertClosed(t, "UDP", sockets.udp)
}

// TestProbeBothProtocolsUsesTheNetwork: probeBothProtocols is probeWith over
// the real listeners, holding addr on both protocols until Close.
func TestProbeBothProtocolsUsesTheNetwork(t *testing.T) {
	port, err := ReserveTCPPort()
	if err != nil {
		t.Fatalf("ReserveTCPPort: %v", err)
	}
	addr := net.JoinHostPort(loopbackHost, strconv.Itoa(port))
	probe, err := probeBothProtocols(addr)
	if err != nil {
		t.Fatalf("probe %s: %v", addr, err)
	}
	defer func() { _ = probe.Close() }()
	if pc, err := net.ListenPacket("udp", addr); err == nil {
		_ = pc.Close()
		t.Fatal("probeBothProtocols does not hold UDP")
	}
}

func TestLockPortAcrossProcessesAdmitsOneHolder(t *testing.T) {
	port, err := ReserveTCPPort()
	if err != nil {
		t.Fatalf("ReserveTCPPort: %v", err)
	}
	// This process already holds port's lock, so a second take must fail.
	if l, err := lockPortAcrossProcesses(port); err == nil {
		_ = l.Close()
		t.Fatalf("locked port %d a second time", port)
	}

	// A lock that is released can be taken again. A number far above any
	// window base is used so no reservation in this process holds it.
	const free = 65000
	first, err := lockPortAcrossProcesses(free)
	if err != nil {
		t.Fatalf("lock %d: %v", free, err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("release %d: %v", free, err)
	}
	again, err := lockPortAcrossProcesses(free)
	if err != nil {
		t.Fatalf("relock %d after release: %v", free, err)
	}
	_ = again.Close()
}

func TestAllocatorReserveSkipsPortsLockedElsewhere(t *testing.T) {
	base := 32768 - portWindow
	a := newAllocator(fixedRangeFile(t, "32768 60999"), func(port int) (io.Closer, error) {
		if port == base {
			return nil, errors.New("address already in use")
		}
		return unlocked(port)
	})
	var probed []string
	ports, err := a.reserve("UDP", 1, func(addr string) (io.Closer, error) {
		probed = append(probed, addr)
		return closerFunc(func() error { return nil }), nil
	})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if len(ports) != 1 || ports[0] != base+1 {
		t.Fatalf("ports = %v, want [%d] after skipping the port locked elsewhere", ports, base+1)
	}
	if len(probed) != 1 {
		t.Fatalf("probed %v; a port locked elsewhere must not even be probed", probed)
	}
}

// TestAllocatorReserveLockCollisionsDoNotSpendAttempts pins that ports held by
// other test processes do not count against the bind-verify budget. Every
// process walks the window from its base, so a late process in a parallel
// `go test ./...` meets every port the earlier ones still hold -- more than
// maxReservationAttempts of them in a busy module -- before reaching a free
// one. Spending an attempt per held lock made that process fail its first
// reservation with "bind: address already in use" from the lock socket.
func TestAllocatorReserveLockCollisionsDoNotSpendAttempts(t *testing.T) {
	base := 32768 - portWindow
	held := maxReservationAttempts + 10
	a := newAllocator(fixedRangeFile(t, "32768 60999"), func(port int) (io.Closer, error) {
		if port < base+held {
			return nil, errors.New("address already in use")
		}
		return unlocked(port)
	})
	probed := 0
	ports, err := a.reserve("UDP", 2, func(string) (io.Closer, error) {
		probed++
		return closerFunc(func() error { return nil }), nil
	})
	if err != nil {
		t.Fatalf("reserve past %d ports held elsewhere: %v", held, err)
	}
	if want := []int{base + held, base + held + 1}; !slices.Equal(ports, want) {
		t.Fatalf("ports = %v, want %v", ports, want)
	}
	if probed != 2 {
		t.Fatalf("probed %d ports, want 2: a port locked elsewhere must not be probed", probed)
	}
}

// TestAllocatorReserveLockCollisionsEndAtWindowExhaustion pins that, with
// lock collisions outside the attempt budget, the window itself bounds the
// walk: a window held entirely by other processes reports exhaustion rather
// than looping.
func TestAllocatorReserveLockCollisionsEndAtWindowExhaustion(t *testing.T) {
	a := newAllocator(fixedRangeFile(t, "32768 60999"), func(int) (io.Closer, error) {
		return nil, errors.New("address already in use")
	})
	_, err := a.reserve("UDP", 1, func(string) (io.Closer, error) {
		t.Fatal("a port locked elsewhere must not be probed")
		return nil, nil
	})
	var exhaustedErr *PortExhaustedError
	if !errors.As(err, &exhaustedErr) {
		t.Fatalf("err = %v, want *PortExhaustedError", err)
	}
}

// TestAllocatorReserveLockLifetimes: a handed-out port's lock is kept for the
// life of the process; the lock of a port that failed its bind, or of every
// port in a batch that fell short, is released for others to take.
func TestAllocatorReserveLockLifetimes(t *testing.T) {
	base := 32768 - portWindow
	released := map[int]bool{}
	var mu sync.Mutex
	tracking := func(port int) (io.Closer, error) {
		return closerFunc(func() error {
			mu.Lock()
			released[port] = true
			mu.Unlock()
			return nil
		}), nil
	}

	a := newAllocator(fixedRangeFile(t, "32768 60999"), tracking)
	ports, err := a.reserve("TCP", 1, func(addr string) (io.Closer, error) {
		if addr == net.JoinHostPort(loopbackHost, strconv.Itoa(base)) {
			return nil, errors.New("address already in use")
		}
		return closerFunc(func() error { return nil }), nil
	})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if !released[base] {
		t.Errorf("the lock of unbindable port %d was kept", base)
	}
	if released[ports[0]] || len(a.locks) != 1 {
		t.Errorf("handed-out port %d: released=%v, held locks=%d; want kept", ports[0], released[ports[0]], len(a.locks))
	}

	short := newAllocator(fixedRangeFile(t, "32768 60999"), tracking)
	clear(released)
	bound := 0
	_, err = short.reserve("TCP", maxReservationAttempts+1, func(string) (io.Closer, error) {
		bound++
		return closerFunc(func() error { return nil }), nil
	})
	var reservationErr *PortReservationError
	if !errors.As(err, &reservationErr) {
		t.Fatalf("err = %v, want *PortReservationError", err)
	}
	if len(released) != bound || len(short.locks) != 0 {
		t.Errorf("short batch: released %d of %d locks, still holding %d; want all released", len(released), bound, len(short.locks))
	}
}

func TestAllocatorReserveReleasesLocksOnWindowError(t *testing.T) {
	released := 0
	a := newAllocator(fixedRangeFile(t, "32768 60999"), func(int) (io.Closer, error) {
		return closerFunc(func() error { released++; return nil }), nil
	})
	// Exhaust all but one candidate so the second draw of the batch fails.
	a.next = 32768 - 1
	_, err := a.reserve("TCP", 2, func(string) (io.Closer, error) {
		return closerFunc(func() error { return nil }), nil
	})
	var exhausted *PortExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("err = %v, want *PortExhaustedError", err)
	}
	if released != 1 || len(a.locks) != 0 {
		t.Errorf("released %d locks, holding %d; want the one taken lock released", released, len(a.locks))
	}
}

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
	a := newAllocator(path, unlocked)

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
	a := newAllocator(fixedRangeFile(t, "32768 60999"), unlocked)

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
	a := newAllocator(fixedRangeFile(t, "32768 60999"), unlocked)
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
	a := newAllocator(fixedRangeFile(t, strconv.Itoa(lowestReservablePort+portWindow-1)+" 60999"), unlocked)
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
	a := newAllocator(fixedRangeFile(t, "32768 60999"), unlocked)
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
	a := newAllocator(filepath.Join(t.TempDir(), "absent"), unlocked)
	if _, err := a.nextPort(); err == nil {
		t.Fatal("nextPort succeeded without a readable ephemeral range")
	}
}

func TestAllocatorReserveBindVerifies(t *testing.T) {
	a := newAllocator(fixedRangeFile(t, "32768 60999"), unlocked)
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
	a := newAllocator(fixedRangeFile(t, "32768 60999"), unlocked)
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
	a := newAllocator(fixedRangeFile(t, "32768 60999"), unlocked)
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
	a := newAllocator(fixedRangeFile(t, "32768 60999"), unlocked)
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
	a := newAllocator(fixedRangeFile(t, "32768 60999"), unlocked)
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
	a := newAllocator(fixedRangeFile(t, "1024 60999"), unlocked)
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

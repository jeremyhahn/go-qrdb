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
	"fmt"
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

const (
	testEphemeralLow = 32768
	testWindow       = 8192
	testWindowBase   = testEphemeralLow - testWindow
	testRange        = "32768 60999"
	testHost         = "127.0.0.1"

	// helperEnv selects the re-exec helper mode of this test binary.
	helperEnv = "NETPORT_TEST_HELPER"
	// reserveHelperMode makes the re-exec'd binary reserve helperPorts ports
	// through Reserve, print them, and exit holding nothing.
	reserveHelperMode = "reserve"
	helperPorts       = 4
)

// TestMain runs the second process of the cross-process disjointness test when
// helperEnv asks for it, and the tests otherwise.
func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == reserveHelperMode {
		ports, err := Reserve(testHost, helperPorts)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		for _, port := range ports {
			fmt.Fprintln(os.Stdout, port)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// unlocked is a lock that always succeeds and holds nothing.
func unlocked(int) (io.Closer, error) { return closerFunc(func() error { return nil }), nil }

func newTestAllocator(t *testing.T, lock locker) *windowAllocator {
	t.Helper()
	return newWindowAllocator(writeEphemeralRange(t, testRange), testWindow, lowestReservablePort, lock)
}

func TestReserve_CertifiesEveryPortOnBothProtocolsAtTheHost(t *testing.T) {
	log := recordProbes(t)
	ports, err := Reserve(testHost, 2)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if len(ports) != 2 {
		t.Fatalf("ports = %v, want 2", ports)
	}
	certified := map[probeCall]bool{}
	for _, call := range log.recorded() {
		certified[call] = true
	}
	for _, port := range ports {
		for _, network := range []string{NetworkTCP, NetworkUDP} {
			want := probeCall{network: network, host: testHost, port: strconv.Itoa(port)}
			if !certified[want] {
				t.Errorf("port %d was never probed on %s at %s", port, network, testHost)
			}
		}
	}
}

func TestReserve_KeepsTheCrossProcessLock(t *testing.T) {
	ports, err := Reserve(testHost, 1)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	// This process already holds the port's lock, so a second take must fail.
	if l, err := lockAcrossProcesses(ports[0]); err == nil {
		_ = l.Close()
		t.Fatalf("locked reserved port %d a second time", ports[0])
	}
}

func TestReserve_DisjointAcrossProcesses(t *testing.T) {
	handedOut, err := Reserve(testHost, helperPorts)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	ours := make(map[int]struct{}, len(handedOut))
	for _, p := range handedOut {
		ours[p] = struct{}{}
	}

	// #nosec G204,G702 -- re-exec of this test binary: argv[0] is os.Args[0], the path `go test` invoked, with no arguments; the helper mode travels in a compile-time-literal environment entry. No shell is involved.
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), helperEnv+"="+reserveHelperMode)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("second process: %v: %s", err, stderr.String())
	}
	child := strings.Fields(string(out))
	if len(child) != helperPorts {
		t.Fatalf("second process reserved %v, want %d ports", child, helperPorts)
	}

	// The comparison is against the ports this process handed out, not
	// against a failed lock: once the child exits, a concurrent `go test`
	// package process may legitimately lock a port the child released.
	for _, s := range child {
		port, err := strconv.Atoi(s)
		if err != nil {
			t.Fatalf("second process printed %q: %v", s, err)
		}
		if _, taken := ours[port]; taken {
			t.Errorf("port %d was handed out by this process AND by a concurrent one", port)
		}
	}
}

func TestReserve_ConcurrentCallersGetDisjointPorts(t *testing.T) {
	const goroutines, perGoroutine = 8, 4
	var wg sync.WaitGroup
	results := make([][]int, goroutines)
	errs := make([]error, goroutines)
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = Reserve(testHost, perGoroutine)
		}()
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

func TestLockAcrossProcesses_AdmitsOneHolderUntilReleased(t *testing.T) {
	// A number far above any window base, so no reservation in this process
	// holds its lock.
	const free = 65000
	first, err := lockAcrossProcesses(free)
	if err != nil {
		t.Fatalf("lock %d: %v", free, err)
	}
	if second, err := lockAcrossProcesses(free); err == nil {
		_ = second.Close()
		t.Fatalf("locked %d twice", free)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("release %d: %v", free, err)
	}
	again, err := lockAcrossProcesses(free)
	if err != nil {
		t.Fatalf("relock %d after release: %v", free, err)
	}
	_ = again.Close()
}

func TestWindowAllocator_RejectsBadNetworksBeforeDrawing(t *testing.T) {
	a := newTestAllocator(t, func(int) (io.Closer, error) {
		t.Fatal("a reservation with no usable network took a lock")
		return nil, nil
	})
	var empty *NoProbeNetworkError
	if _, err := a.reserve(testHost, 1); !errors.As(err, &empty) {
		t.Fatalf("err = %v, want *NoProbeNetworkError", err)
	}
	var unknown *UnknownProbeNetworkError
	if _, err := a.reserve(testHost, 1, "sctp"); !errors.As(err, &unknown) {
		t.Fatalf("err = %v, want *UnknownProbeNetworkError", err)
	}
}

func TestWindowAllocator_SkipsPortsLockedElsewhere(t *testing.T) {
	a := newTestAllocator(t, func(port int) (io.Closer, error) {
		if port == testWindowBase {
			return nil, errors.New("address already in use")
		}
		return unlocked(port)
	})
	var probed []string
	network := fakeNetwork(t, func(addr string) (io.Closer, error) {
		probed = append(probed, addr)
		return binds(addr)
	})

	ports, err := a.reserve(testHost, 1, network)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if len(ports) != 1 || ports[0] != testWindowBase+1 {
		t.Fatalf("ports = %v, want [%d] after skipping the port locked elsewhere", ports, testWindowBase+1)
	}
	if len(probed) != 1 {
		t.Fatalf("probed %v; a port locked elsewhere must not even be probed", probed)
	}
}

// TestWindowAllocator_SkipsMorePortsLockedElsewhereThanTheProbeBudget guards
// the window's own candidate budget: every process walks the window from the
// same base, so one that starts after others hold their ports must skip all of
// them, far more than FreePortOn's budget, before it reaches a free one.
func TestWindowAllocator_SkipsMorePortsLockedElsewhereThanTheProbeBudget(t *testing.T) {
	const lockedElsewhere = 10 * freePortAttempts
	a := newTestAllocator(t, func(port int) (io.Closer, error) {
		if port < testWindowBase+lockedElsewhere {
			return nil, errors.New("address already in use")
		}
		return unlocked(port)
	})

	ports, err := a.reserve(testHost, 3, fakeNetwork(t, binds))
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	want := []int{testWindowBase + lockedElsewhere, testWindowBase + lockedElsewhere + 1, testWindowBase + lockedElsewhere + 2}
	for i := range want {
		if ports[i] != want[i] {
			t.Fatalf("ports = %v, want %v", ports, want)
		}
	}
}

// TestWindowAllocator_LockCollisionsDoNotSpendAttempts pins that ports held by
// other processes do not count against the bind-probe budget. A late process
// in a parallel `go test ./...` meets every port the earlier ones still hold
// -- more than windowReservationAttempts of them in a busy module -- before it
// reaches a free one; spending an attempt per held lock failed its first
// reservation.
func TestWindowAllocator_LockCollisionsDoNotSpendAttempts(t *testing.T) {
	const lockedElsewhere = windowReservationAttempts + 10
	a := newTestAllocator(t, func(port int) (io.Closer, error) {
		if port < testWindowBase+lockedElsewhere {
			return nil, errors.New("address already in use")
		}
		return unlocked(port)
	})
	probed := 0
	network := fakeNetwork(t, func(addr string) (io.Closer, error) {
		probed++
		return binds(addr)
	})

	ports, err := a.reserve(testHost, 2, network)
	if err != nil {
		t.Fatalf("reserve past %d ports locked elsewhere: %v", lockedElsewhere, err)
	}
	if want := []int{testWindowBase + lockedElsewhere, testWindowBase + lockedElsewhere + 1}; !slices.Equal(ports, want) {
		t.Fatalf("ports = %v, want %v", ports, want)
	}
	if probed != 2 {
		t.Fatalf("probed %d ports, want 2: a port locked elsewhere must not be probed", probed)
	}
}

// TestWindowAllocator_LockCollisionsEndAtWindowExhaustion pins that, with lock
// collisions outside the attempt budget, the window itself bounds the walk: a
// window held entirely by other processes reports exhaustion rather than
// looping.
func TestWindowAllocator_LockCollisionsEndAtWindowExhaustion(t *testing.T) {
	a := newTestAllocator(t, func(int) (io.Closer, error) {
		return nil, errors.New("address already in use")
	})
	network := fakeNetwork(t, func(string) (io.Closer, error) {
		t.Fatal("a port locked elsewhere must not be probed")
		return nil, nil
	})

	_, err := a.reserve(testHost, 1, network)
	var exhausted *PortExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("err = %v, want *PortExhaustedError", err)
	}
}

// TestWindowAllocator_LockLifetimes checks that the lock of a port handed out
// is kept for the life of the process, while the lock of an unbindable
// candidate, and every lock of a batch that fell short, is released.
func TestWindowAllocator_LockLifetimes(t *testing.T) {
	var mu sync.Mutex
	released := map[int]bool{}
	tracking := func(port int) (io.Closer, error) {
		return closerFunc(func() error {
			mu.Lock()
			released[port] = true
			mu.Unlock()
			return nil
		}), nil
	}

	a := newTestAllocator(t, tracking)
	busyBase := fakeNetwork(t, func(addr string) (io.Closer, error) {
		if addr == net.JoinHostPort(testHost, strconv.Itoa(testWindowBase)) {
			return nil, errors.New("address already in use")
		}
		return binds(addr)
	})
	ports, err := a.reserve(testHost, 1, busyBase)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if !released[testWindowBase] {
		t.Errorf("the lock of unbindable port %d was kept", testWindowBase)
	}
	if released[ports[0]] || len(a.locks) != 1 {
		t.Errorf("handed-out port %d: released=%v, held locks=%d; want kept", ports[0], released[ports[0]], len(a.locks))
	}

	short := newTestAllocator(t, tracking)
	clear(released)
	bound := 0
	counting := fakeNetwork(t, func(addr string) (io.Closer, error) {
		bound++
		return binds(addr)
	})
	_, err = short.reserve(testHost, windowReservationAttempts+1, counting)
	var reservationErr *PortReservationError
	if !errors.As(err, &reservationErr) {
		t.Fatalf("err = %v, want *PortReservationError", err)
	}
	if len(released) != bound || len(short.locks) != 0 {
		t.Errorf("short batch: released %d of %d locks, still holding %d; want all released", len(released), bound, len(short.locks))
	}
}

func TestWindowAllocator_ReleasesLocksOnWindowError(t *testing.T) {
	released := 0
	a := newTestAllocator(t, func(int) (io.Closer, error) {
		return closerFunc(func() error { released++; return nil }), nil
	})
	// Exhaust all but one candidate so the second draw of the batch fails.
	a.next = testEphemeralLow - 1

	_, err := a.reserve(testHost, 2, fakeNetwork(t, binds))
	var exhausted *PortExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("err = %v, want *PortExhaustedError", err)
	}
	if exhausted.Low != testEphemeralLow || exhausted.Base != testWindowBase {
		t.Errorf("window = [%d,%d), want [%d,%d)", exhausted.Base, exhausted.Low, testWindowBase, testEphemeralLow)
	}
	if released != 1 || len(a.locks) != 0 {
		t.Errorf("released %d locks, holding %d; want the one taken lock released", released, len(a.locks))
	}
}

func TestWindowAllocator_BindVerifiesEveryPortAtTheHost(t *testing.T) {
	a := newTestAllocator(t, unlocked)
	var probed []string
	network := fakeNetwork(t, func(addr string) (io.Closer, error) {
		probed = append(probed, addr)
		return binds(addr)
	})

	ports, err := a.reserve(testHost, 3, network)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if len(ports) != 3 || len(probed) != 3 {
		t.Fatalf("ports = %v, probed %v; want three ports, one bind each", ports, probed)
	}
	for i, addr := range probed {
		if want := net.JoinHostPort(testHost, strconv.Itoa(ports[i])); addr != want {
			t.Errorf("probe %d bound %q, want %q", i, addr, want)
		}
	}
}

func TestWindowAllocator_HoldsProbesUntilTheBatchIsDecided(t *testing.T) {
	a := newTestAllocator(t, unlocked)
	open, peak := 0, 0
	network := fakeNetwork(t, func(string) (io.Closer, error) {
		open++
		peak = max(peak, open)
		return closerFunc(func() error { open--; return nil }), nil
	})

	if _, err := a.reserve(testHost, 4, network, network); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if peak != 8 {
		t.Errorf("peak open probes = %d, want 8 (both probes of all four ports held until the batch completes)", peak)
	}
	if open != 0 {
		t.Errorf("%d probe sockets left open", open)
	}
}

func TestWindowAllocator_SkipsBusyPorts(t *testing.T) {
	a := newTestAllocator(t, unlocked)
	network := fakeNetwork(t, func(addr string) (io.Closer, error) {
		if addr == net.JoinHostPort(testHost, strconv.Itoa(testWindowBase)) {
			return nil, errors.New("address already in use")
		}
		return binds(addr)
	})

	ports, err := a.reserve(testHost, 1, network)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if len(ports) != 1 || ports[0] != testWindowBase+1 {
		t.Errorf("ports = %v, want [%d] after skipping the busy candidate", ports, testWindowBase+1)
	}
}

func TestWindowAllocator_FailsWhenNothingBinds(t *testing.T) {
	a := newTestAllocator(t, unlocked)
	bindErr := errors.New("address already in use")
	network := fakeNetwork(t, func(string) (io.Closer, error) { return nil, bindErr })

	_, err := a.reserve(testHost, 2, network)
	var reservationErr *PortReservationError
	if !errors.As(err, &reservationErr) {
		t.Fatalf("err = %v, want *PortReservationError", err)
	}
	if reservationErr.Requested != 2 || reservationErr.Reserved != 0 || reservationErr.Host != testHost ||
		len(reservationErr.Networks) != 1 || reservationErr.Networks[0] != network {
		t.Errorf("unexpected error detail: %+v", reservationErr)
	}
	if !errors.Is(err, bindErr) {
		t.Error("error does not unwrap to the last bind failure")
	}
}

func TestWindowAllocator_ZeroPortsProbesNothing(t *testing.T) {
	a := newTestAllocator(t, unlocked)
	network := fakeNetwork(t, func(string) (io.Closer, error) {
		t.Fatal("a reservation of zero ports probed a port")
		return nil, nil
	})

	ports, err := a.reserve(testHost, 0, network)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if len(ports) != 0 {
		t.Errorf("ports = %v, want none", ports)
	}
}

func TestWindowAllocator_WindowErrorProbesNothing(t *testing.T) {
	a := newWindowAllocator(writeEphemeralRange(t, "1024 60999"), testWindow, lowestReservablePort, unlocked)
	network := fakeNetwork(t, func(string) (io.Closer, error) {
		t.Fatal("a reservation probed a port from an unusable window")
		return nil, nil
	})

	_, err := a.reserve(testHost, 1, network)
	var windowErr *PortWindowError
	if !errors.As(err, &windowErr) {
		t.Fatalf("err = %v, want *PortWindowError", err)
	}
	if windowErr.Window != testWindow {
		t.Errorf("Window = %d, want %d", windowErr.Window, testWindow)
	}
}

func TestWindowAllocator_RangeErrorProbesNothing(t *testing.T) {
	a := newWindowAllocator(filepath.Join(t.TempDir(), "absent"), testWindow, lowestReservablePort, unlocked)
	network := fakeNetwork(t, func(string) (io.Closer, error) {
		t.Fatal("a reservation probed a port without knowing the ephemeral range")
		return nil, nil
	})

	_, err := a.reserve(testHost, 1, network)
	var rangeErr *EphemeralRangeError
	if !errors.As(err, &rangeErr) {
		t.Fatalf("err = %v, want *EphemeralRangeError", err)
	}
}

func TestWindowAllocator_NextPortDrawsBelowTheEphemeralRange(t *testing.T) {
	a := newTestAllocator(t, unlocked)
	first, err := a.nextPort()
	if err != nil {
		t.Fatalf("nextPort: %v", err)
	}
	if first != testWindowBase {
		t.Errorf("first candidate = %d, want the window base %d", first, testWindowBase)
	}

	seen := map[int]struct{}{first: {}}
	for range 64 {
		port, err := a.nextPort()
		if err != nil {
			t.Fatalf("nextPort: %v", err)
		}
		if port >= testEphemeralLow || port < testWindowBase {
			t.Fatalf("candidate %d is outside the window [%d,%d)", port, testWindowBase, testEphemeralLow)
		}
		if _, dup := seen[port]; dup {
			t.Fatalf("candidate %d handed out twice", port)
		}
		seen[port] = struct{}{}
	}
}

func TestWindowAllocator_NextPortSkipsAlreadyReserved(t *testing.T) {
	a := newTestAllocator(t, unlocked)
	a.reserved[testWindowBase] = struct{}{}
	a.reserved[testWindowBase+1] = struct{}{}

	port, err := a.nextPort()
	if err != nil {
		t.Fatalf("nextPort: %v", err)
	}
	if port != testWindowBase+2 {
		t.Errorf("port = %d, want %d (the first unreserved candidate)", port, testWindowBase+2)
	}
}

func TestWindowAllocator_EphemeralLowIsReadOnce(t *testing.T) {
	path := writeEphemeralRange(t, testRange)
	a := newWindowAllocator(path, testWindow, lowestReservablePort, unlocked)

	low, err := a.ephemeralLow()
	if err != nil || low != testEphemeralLow {
		t.Fatalf("ephemeralLow = (%d, %v), want %d", low, err, testEphemeralLow)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if again, err := a.ephemeralLow(); err != nil || again != low {
		t.Fatalf("ephemeralLow re-read the file: got (%d, %v)", again, err)
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
			low, err := readEphemeralLow(writeEphemeralRange(t, tc.contents))
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

func TestReadEphemeralLow_MissingFile(t *testing.T) {
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

func TestWindowAllocator_ConcurrentCallersGetDisjointPorts(t *testing.T) {
	const goroutines, perGoroutine = 8, 4
	a := newTestAllocator(t, unlocked)
	network := fakeNetwork(t, binds)

	var wg sync.WaitGroup
	results := make([][]int, goroutines)
	errs := make([]error, goroutines)
	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = a.reserve(testHost, perGoroutine, network)
		}()
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

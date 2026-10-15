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
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
)

// Reserve tracks every port it hands out for the lifetime of the process and
// draws candidates sequentially from a window BELOW the kernel's ephemeral port
// range (/proc/sys/net/ipv4/ip_local_port_range), because three distinct races
// defeat a bind to port 0:
//
//  1. Intra-process re-hand: a reservation that closes its probe sockets
//     before returning lets the kernel hand a previously returned port to a
//     LATER call, so two servers in one process collide. The process-wide
//     reservation table rules that out for every caller at once.
//
//  2. Kernel ephemeral-source steal (TOCTOU): a port obtained by binding ":0"
//     lies inside the kernel's ephemeral range, so between the probe closing
//     and the server binding the port, ANY outbound connection from the
//     process (Raft peer dials, health polls) can be assigned that exact
//     number as its source port, and the server then fails with "address
//     already in use" (seen as a TestCluster_SnapshotTriggered flake,
//     2026-06-11). No in-process table can see kernel source-port assignment;
//     outbound connections never draw from below the ephemeral range, so the
//     window lies there.
//
//  3. Cross-process re-hand: `go test` runs packages as concurrent processes
//     that all walk the same window from the same base, and each one's bind
//     check passes while the other's probe is closed. Both then handed the
//     same port out: their servers collided, or two Raft clusters dialled
//     each other's hosts and never elected a leader. Every handed-out port is
//     therefore also LOCKED across processes (see lockAcrossProcesses), held
//     until the process exits.
//
// Do not "simplify" this back to binding port 0.

const (
	// portWindow is how many ports below the ephemeral floor Reserve draws
	// from. A full integration run reserves a few hundred ports; 8192 leaves
	// ample headroom while staying far above the well-known ports.
	portWindow = 8192

	// lowestReservablePort is the floor of the window: reaching below it would
	// collide with well-known service ports.
	lowestReservablePort = 1024

	// windowReservationAttempts bounds how many candidates one Reserve call
	// bind-probes before giving up. A candidate another process has locked is
	// not probed and spends no attempt: every process walks the window from
	// the same base, so one that starts after others hold their ports skips
	// all of them before it reaches a free one, and only the window's end
	// bounds that walk.
	windowReservationAttempts = 1000

	// ephemeralRangePath is the kernel file describing the ephemeral port
	// range that reserved ports must avoid.
	ephemeralRangePath = "/proc/sys/net/ipv4/ip_local_port_range"

	// portLockPrefix names the abstract Unix sockets that lock a port across
	// processes. The leading "@" selects Linux's abstract namespace, which has
	// no filesystem presence and, like the ports themselves, is scoped to the
	// network namespace -- so a container's processes never contend with the
	// host's, while two test processes on one host always do. The name predates
	// this package; keeping it lets a process built before the move still
	// contend with one built after it.
	portLockPrefix = "@go-qrdb/qrdbdharness/port/"
)

// reserveNetworks are the protocols every reserved port is certified free on.
// One table hands each number out once for either protocol, and a REST
// listener takes its port on TCP while its HTTP/3 sibling co-binds the same
// number on UDP (go-qrdb#248, go-qrdb#257), so certifying both lets any caller
// use a reserved port for any listener family.
var reserveNetworks = []string{NetworkTCP, NetworkUDP}

// defaultWindow is the process-wide reservation table every Reserve call uses;
// being a singleton is what makes the reservation process-wide.
var defaultWindow = newWindowAllocator(ephemeralRangePath, portWindow, lowestReservablePort, lockAcrossProcesses)

// Reserve reserves n ports at host from the process-wide window below the
// kernel's ephemeral range, each certified free on TCP and on UDP and never
// handed out twice by this process or by any other in the same network
// namespace. It is for processes that start many listeners and cannot afford
// the races FreePortOn leaves open (see the top of this file); the window is
// Linux-only, as it reads the ephemeral range from /proc.
func Reserve(host string, n int) ([]int, error) {
	return defaultWindow.reserve(host, n, reserveNetworks...)
}

// locker takes the cross-process lock for a port, failing when another holder
// has it. Closing the returned lock releases it.
type locker func(port int) (io.Closer, error)

// lockAcrossProcesses binds the abstract Unix socket named for port. The
// kernel admits one binder per name and releases it when the socket closes or
// its process exits -- a crash included, so a lock can never be orphaned. Go
// opens sockets close-on-exec, so a spawned daemon never inherits a lock.
func lockAcrossProcesses(port int) (io.Closer, error) {
	return net.Listen("unix", portLockPrefix+strconv.Itoa(port))
}

// windowAllocator is the reservation table behind Reserve. It is a type rather
// than a set of package variables so its failure modes are testable against a
// fixture range file and a fake lock.
type windowAllocator struct {
	rangePath string
	window    int
	lowest    int
	attempts  int
	lock      locker

	lowOnce sync.Once
	low     int
	lowErr  error

	mu       sync.Mutex
	reserved map[int]struct{}
	next     int
	// locks keeps every handed-out port's cross-process lock referenced for
	// the life of the process; a dropped listener could be closed by its
	// finalizer, releasing a port another process would then take.
	locks []io.Closer
}

// newWindowAllocator returns an allocator drawing window ports below the
// ephemeral floor read from rangePath, never below lowest, and locking each
// port it hands out with lock.
func newWindowAllocator(rangePath string, window, lowest int, lock locker) *windowAllocator {
	return &windowAllocator{
		rangePath: rangePath,
		window:    window,
		lowest:    lowest,
		attempts:  windowReservationAttempts,
		lock:      lock,
		reserved:  make(map[int]struct{}),
	}
}

// reserve reserves n unique ports at host, each free on every named network.
//
// A candidate is taken only when this process wins its cross-process lock AND
// it binds. The lock comes first: a port another process has handed out may be
// momentarily unbound (its probe closed, its server not yet listening), so a
// bind check alone would certify it again. A candidate locked elsewhere or busy
// outside the participating processes is skipped for the next one. The probes
// stay open until the whole batch is decided; the locks of the ports handed
// out are kept for the life of the process.
func (a *windowAllocator) reserve(host string, n int, networks ...string) ([]int, error) {
	resolved, err := resolveProbes(host, networks)
	if err != nil {
		return nil, err
	}
	ports := make([]int, 0, n)
	var held, locks closers
	var lastErr error
	// Only a bind probe spends an attempt. A port another process holds is
	// skipped for free: every process walks the window from its base, so a
	// late one meets every port the others still hold before a free one, and
	// in a busy module those outnumber the budget. nextPort's window
	// exhaustion bounds that walk.
	for attempt := 0; len(ports) < n && attempt < a.attempts; {
		port, err := a.nextPort()
		if err != nil {
			_ = held.Close()
			_ = locks.Close()
			return nil, err
		}
		lock, err := a.lock(port)
		if err != nil {
			lastErr = err // handed out by another process; draw the next candidate
			continue
		}
		attempt++
		_, probe, err := holdPort(host, strconv.Itoa(port), resolved)
		if err != nil {
			_ = lock.Close()
			lastErr = err // busy outside the participating processes; draw the next candidate
			continue
		}
		held = append(held, probe)
		locks = append(locks, lock)
		ports = append(ports, port)
	}
	_ = held.Close()
	if len(ports) < n {
		_ = locks.Close()
		return nil, &PortReservationError{Host: host, Networks: networks, Requested: n, Reserved: len(ports), LastErr: lastErr}
	}
	a.mu.Lock()
	a.locks = append(a.locks, locks...)
	a.mu.Unlock()
	return ports, nil
}

// nextPort reserves and returns the next sequential candidate from the window.
// The caller must still lock and bind-verify it -- something outside this
// process could be using it -- and draw again on failure.
func (a *windowAllocator) nextPort() (int, error) {
	low, err := a.ephemeralLow()
	if err != nil {
		return 0, err
	}
	base := low - a.window
	if base < a.lowest {
		return 0, &PortWindowError{EphemeralLow: low, Window: a.window}
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.next == 0 {
		a.next = base
	}
	for a.next < low {
		port := a.next
		a.next++
		if _, taken := a.reserved[port]; taken {
			continue
		}
		a.reserved[port] = struct{}{}
		return port, nil
	}
	return 0, &PortExhaustedError{Window: a.window, Base: base, Low: low}
}

// ephemeralLow returns the lower bound of the kernel's ephemeral port range,
// read once per allocator.
func (a *windowAllocator) ephemeralLow() (int, error) {
	a.lowOnce.Do(func() {
		a.low, a.lowErr = readEphemeralLow(a.rangePath)
	})
	return a.low, a.lowErr
}

// readEphemeralLow parses the lower bound out of the kernel's
// ip_local_port_range file.
func readEphemeralLow(path string) (int, error) {
	// #nosec G304 -- `path` is the package constant ephemeralRangePath in every
	// non-test caller; test callers pass a fixture under t.TempDir().
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, &EphemeralRangeError{Path: path, Reason: "read failed", Err: err}
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return 0, &EphemeralRangeError{Path: path, Reason: "expected two fields, got " + strconv.Itoa(len(fields))}
	}
	low, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, &EphemeralRangeError{Path: path, Reason: "lower bound is not a number", Err: err}
	}
	if low <= 0 {
		return 0, &EphemeralRangeError{Path: path, Reason: "lower bound " + strconv.Itoa(low) + " is out of range"}
	}
	return low, nil
}

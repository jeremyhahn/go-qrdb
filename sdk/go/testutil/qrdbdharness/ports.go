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
	"strconv"
	"strings"
	"sync"
)

// The reservation table below tracks every port handed out by ReserveTCPPorts
// / ReserveUDPPorts for the lifetime of the test process, and candidates are
// drawn sequentially from a window BELOW the kernel's ephemeral port range
// (/proc/sys/net/ipv4/ip_local_port_range).
//
// Two distinct races motivated this design:
//
//  1. Intra-process re-hand: an allocate call that closes its listeners
//     before returning lets the OS re-hand a previously-returned port to a
//     LATER call, so two servers in the same node could collide on a port.
//     The process-wide reservation table eliminates that for every call
//     site at once. TCP and UDP share the table: skipping a number the
//     other protocol used is harmless and keeps the logic simple.
//
//  2. Kernel ephemeral-source steal (TOCTOU): a port obtained by binding
//     ":0" lies inside the kernel's ephemeral range, so between the
//     allocator closing its probe listener and the daemon binding the
//     port, ANY outbound connection from a node process (Raft peer dials,
//     health polls) can be assigned that exact number as its source port —
//     the daemon then fails with "address already in use" (seen as a
//     TestCluster_SnapshotTriggered flake, 2026-06-11). No in-process
//     table can see kernel source-port assignment; the robust fix is to
//     allocate listen ports OUTSIDE the ephemeral range, which outbound
//     connections never draw from.
//
//  3. Cross-process re-hand: the reservation table is per PROCESS, and every
//     process walks the same window from the same base. `go test` runs
//     packages as concurrent processes, so two of them each bind-verified
//     the same candidate in turn -- each probe closed before the other's
//     ran -- and both handed it out. Their servers then collided ("listen
//     udp ...: bind: address already in use") or, worse, two Raft clusters
//     dialled each other's hosts and never elected a leader. Each reserved
//     port is therefore also LOCKED across processes by binding an abstract
//     Unix socket named for it (see lockPortAcrossProcesses), held until the
//     process exits.
//
// Do not "simplify" this back to net.Listen(":0").

const (
	// portWindow is how many ports below the ephemeral floor the allocators
	// draw from. A full integration run allocates a few hundred ports; 8192
	// leaves ample headroom while staying far above the well-known/registered
	// ranges.
	portWindow = 8192

	// lowestReservablePort is the floor of the allocation window: reaching
	// below it would collide with well-known service ports.
	lowestReservablePort = 1024

	// maxReservationAttempts bounds how many candidates a single reservation
	// call will bind-verify before giving up.
	maxReservationAttempts = 1000

	// ephemeralRangePath is the kernel file describing the ephemeral port
	// range that listen ports must avoid.
	ephemeralRangePath = "/proc/sys/net/ipv4/ip_local_port_range"

	// loopbackHost is the address probe listeners bind to.
	loopbackHost = "127.0.0.1"

	// portLockPrefix names the abstract Unix sockets that lock a port across
	// processes. The leading "@" selects Linux's abstract namespace, which
	// has no filesystem presence and, like the ports themselves, is scoped to
	// the network namespace -- so a container's harness never contends with
	// the host's, while two test processes on one host always do.
	portLockPrefix = "@go-qrdb/qrdbdharness/port/"
)

// portLocker takes the cross-process lock for a port, failing when another
// holder has it. Closing the returned lock releases it.
type portLocker func(port int) (io.Closer, error)

// lockPortAcrossProcesses binds the abstract Unix socket named for port. The
// kernel admits one binder per name and releases it when the socket closes or
// its process exits -- a crash included, so a lock can never be orphaned. The
// harness already requires Linux (it reads the ephemeral range from /proc), and
// Go opens sockets close-on-exec, so a spawned daemon never inherits a lock.
func lockPortAcrossProcesses(port int) (io.Closer, error) {
	return net.Listen("unix", portLockPrefix+strconv.Itoa(port))
}

// allocator is the process-wide port reservation table. It is a type rather
// than a set of package variables purely so its failure modes are testable;
// production callers always use the defaultAllocator singleton, which is what
// makes the reservation process-wide.
type allocator struct {
	rangePath string
	lock      portLocker

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

// defaultAllocator is the singleton every exported reservation call uses.
var defaultAllocator = newAllocator(ephemeralRangePath, lockPortAcrossProcesses)

// newAllocator returns an allocator that reads its ephemeral floor from path
// and locks each port it hands out with lock.
func newAllocator(path string, lock portLocker) *allocator {
	return &allocator{rangePath: path, lock: lock, reserved: make(map[int]struct{})}
}

// ReserveTCPPort reserves a single free TCP port. See ReserveTCPPorts.
func ReserveTCPPort() (int, error) {
	ports, err := ReserveTCPPorts(1)
	if err != nil {
		return 0, err
	}
	return ports[0], nil
}

// ReserveTCPPorts reserves n free TCP ports for server listen addresses. Ports
// are drawn from the reserved non-ephemeral window and bind-verified before
// being returned; probe listeners are held open until the whole batch is
// determined, and no port is ever handed out twice by this process or by any
// other harness process in the same network namespace.
//
// Every port is verified free on UDP as well as TCP (see probeBothProtocols),
// so a REST admin port -- whose HTTP/3 sibling binds the same number on UDP --
// is safe to take from here.
func ReserveTCPPorts(n int) ([]int, error) {
	return defaultAllocator.reserve("TCP", n, probeBothProtocols)
}

// ReserveUDPPort reserves a single free UDP port. See ReserveUDPPorts.
func ReserveUDPPort() (int, error) {
	ports, err := ReserveUDPPorts(1)
	if err != nil {
		return 0, err
	}
	return ports[0], nil
}

// ReserveUDPPorts reserves n free UDP ports for Raft/QUIC listen addresses.
// It shares its reservation table, its cross-process lock and its dual-stack
// probe with ReserveTCPPorts, so a number handed out for one protocol is never
// handed out for the other.
func ReserveUDPPorts(n int) ([]int, error) {
	return defaultAllocator.reserve("UDP", n, probeBothProtocols)
}

// probeBothProtocols binds addr on TCP and on UDP and holds both. TCP and UDP
// are separate port namespaces: a number free on one says nothing about the
// other, and a REST listener takes its port on TCP while its HTTP/3 sibling
// co-binds the same number on UDP (go-qrdb#248, go-qrdb#257). Since one table
// hands each number out once for either protocol, certifying it on both lets
// any caller use it for any listener family.
func probeBothProtocols(addr string) (io.Closer, error) {
	return probeWith(net.Listen, net.ListenPacket, addr)
}

// probeWith is probeBothProtocols over the given listen functions, so a test
// can observe the very sockets the probe opened instead of re-binding the
// number, which another process on the host may have taken in between.
func probeWith(
	listenTCP func(network, addr string) (net.Listener, error),
	listenUDP func(network, addr string) (net.PacketConn, error),
	addr string,
) (io.Closer, error) {
	ln, err := listenTCP("tcp", addr)
	if err != nil {
		return nil, err
	}
	pc, err := listenUDP("udp", addr)
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	return closerFunc(func() error { return errors.Join(ln.Close(), pc.Close()) }), nil
}

// closerFunc adapts a function to io.Closer.
type closerFunc func() error

// Close calls f.
func (f closerFunc) Close() error { return f() }

// reserve draws candidates from the window and keeps the ones that bind.
//
// A candidate is taken only when this process wins its cross-process lock AND
// the port binds. The lock comes first: a port another harness process has
// handed out may be momentarily unbound (its probe closed, its server not yet
// listening), so a bind check alone would certify it again. The lock outlives
// the probe and is kept for the life of the process.
func (a *allocator) reserve(network string, n int, listen func(addr string) (io.Closer, error)) ([]int, error) {
	ports := make([]int, 0, n)
	var listeners, locks []io.Closer
	var lastErr error
	for attempt := 0; len(ports) < n && attempt < maxReservationAttempts; attempt++ {
		port, err := a.nextPort()
		if err != nil {
			closeAll(listeners)
			closeAll(locks)
			return nil, err
		}
		lock, err := a.lock(port)
		if err != nil {
			lastErr = err // handed out by another test process; draw the next candidate
			continue
		}
		conn, err := listen(net.JoinHostPort(loopbackHost, strconv.Itoa(port)))
		if err != nil {
			_ = lock.Close()
			lastErr = err // busy outside the test process; draw the next candidate
			continue
		}
		listeners = append(listeners, conn)
		locks = append(locks, lock)
		ports = append(ports, port)
	}
	closeAll(listeners)
	if len(ports) < n {
		closeAll(locks)
		return nil, &PortReservationError{Network: network, Requested: n, Reserved: len(ports), LastErr: lastErr}
	}
	a.mu.Lock()
	a.locks = append(a.locks, locks...)
	a.mu.Unlock()
	return ports, nil
}

// closeAll releases every probe listener held open for a reservation batch.
func closeAll(listeners []io.Closer) {
	for _, l := range listeners {
		_ = l.Close()
	}
}

// nextPort reserves and returns the next sequential candidate from the
// non-ephemeral window. The caller must still bind-verify it — something
// outside this process could be using it — and draw again on failure.
func (a *allocator) nextPort() (int, error) {
	low, err := a.ephemeralLow()
	if err != nil {
		return 0, err
	}
	base := low - portWindow
	if base < lowestReservablePort {
		return 0, &PortWindowError{EphemeralLow: low, Window: portWindow}
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
	return 0, &PortExhaustedError{Window: portWindow, Base: base, Low: low}
}

// ephemeralLow returns the lower bound of the kernel's ephemeral port range,
// read once per allocator.
func (a *allocator) ephemeralLow() (int, error) {
	a.lowOnce.Do(func() {
		a.low, a.lowErr = readEphemeralLow(a.rangePath)
	})
	return a.low, a.lowErr
}

// readEphemeralLow parses the lower bound out of the kernel's
// ip_local_port_range file.
func readEphemeralLow(path string) (int, error) {
	// #nosec G304 -- `path` is the package constant ephemeralRangePath in every
	// non-test caller; the one test caller passes a fixture under t.TempDir().
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

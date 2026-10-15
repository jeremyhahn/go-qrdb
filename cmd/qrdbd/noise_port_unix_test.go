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

//go:build unix

package main

import (
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// reusePort sets SO_REUSEPORT on a portHold listener, so concurrent copies of
// this package's tests each hold the default Noise port at once.
func reusePort(_, _ string, c syscall.RawConn) error {
	var sockErr error
	if err := c.Control(func(fd uintptr) {
		sockErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
	}); err != nil {
		return err
	}
	return sockErr
}

// TestPortHold_TakesThePortFromAHolderItCannotReach: a holder that has the
// port bound but accepts nowhere refuses the guard's dial, so await has no
// connection to wait on. The guard must still take the port when that holder
// lets go, not give up on its first failed listen.
func TestPortHold_TakesThePortFromAHolderItCannotReach(t *testing.T) {
	port := freeHoldPort(t)
	letGo := bindWithoutListen(t, port)
	h := holdPort(port)
	t.Cleanup(h.release)
	// Let await dial, be refused and find the port still taken, so the
	// holder lets go only after the guard has had to fall back on polling.
	time.Sleep(4 * portHoldRetryInterval)
	select {
	case <-h.armed:
		t.Fatalf("the guard reported port %d armed while another socket had it bound", port)
	default:
	}
	letGo()
	awaitArmed(t, h)
	assertPortHeld(t, port)
}

// bindWithoutListen binds port on the IPv4 wildcard address without
// listening or SO_REUSEADDR, so the port is taken but a dial to it is
// refused. It returns the function that closes the socket, which t's cleanup
// also calls.
func bindWithoutListen(t *testing.T, port int) func() {
	t.Helper()
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socket: %v", err)
	}
	var once sync.Once
	letGo := func() { once.Do(func() { _ = unix.Close(fd) }) }
	t.Cleanup(letGo)
	if err := unix.Bind(fd, &unix.SockaddrInet4{Port: port}); err != nil {
		t.Fatalf("bind port %d without listening: %v", port, err)
	}
	return letGo
}

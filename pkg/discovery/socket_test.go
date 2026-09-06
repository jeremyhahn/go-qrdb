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

//go:build !windows

package discovery

import (
	"errors"
	"net"
	"os"
	"syscall"
	"testing"
)

// fdFlag reads one descriptor or status flag word with fcntl. The production
// code sets close-on-exec and non-blocking through two different mechanisms
// depending on the platform — socket(2) type bits on Linux and the BSDs, fcntl
// on darwin — so the tests assert the observable result rather than the call.
func fdFlag(t *testing.T, fd int, cmd uintptr) int {
	t.Helper()
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), cmd, 0)
	if errno != 0 {
		t.Fatalf("fcntl(%d) on fd %d: %v", cmd, fd, errno)
	}
	return int(flags)
}

func TestNewDatagramSocketSetsCloseOnExec(t *testing.T) {
	fd, err := newDatagramSocket(false)
	if err != nil {
		t.Fatalf("newDatagramSocket(false): %v", err)
	}
	defer closeSocket(fd, "test cleanup")

	if fdFlag(t, fd, syscall.F_GETFD)&syscall.FD_CLOEXEC == 0 {
		t.Error("FD_CLOEXEC is not set; the descriptor would leak across exec")
	}
	if fdFlag(t, fd, syscall.F_GETFL)&syscall.O_NONBLOCK != 0 {
		t.Error("O_NONBLOCK is set although nonblock was false")
	}
}

func TestNewDatagramSocketNonblockSetsBothFlags(t *testing.T) {
	fd, err := newDatagramSocket(true)
	if err != nil {
		t.Fatalf("newDatagramSocket(true): %v", err)
	}
	defer closeSocket(fd, "test cleanup")

	if fdFlag(t, fd, syscall.F_GETFD)&syscall.FD_CLOEXEC == 0 {
		t.Error("FD_CLOEXEC is not set; the descriptor would leak across exec")
	}
	if fdFlag(t, fd, syscall.F_GETFL)&syscall.O_NONBLOCK == 0 {
		t.Error("O_NONBLOCK is not set although nonblock was true")
	}
}

// TestNewDatagramSocketIsUsable checks that the descriptor is a working AF_INET
// datagram socket and not merely a descriptor carrying the right flags.
func TestNewDatagramSocketIsUsable(t *testing.T) {
	fd, err := newDatagramSocket(false)
	if err != nil {
		t.Fatalf("newDatagramSocket(false): %v", err)
	}

	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		closeSocket(fd, "test bind")
		t.Fatalf("bind loopback: %v", err)
	}

	file := os.NewFile(uintptr(fd), "datagram-socket-test")
	conn, err := net.FilePacketConn(file)
	if err := file.Close(); err != nil {
		t.Errorf("close file wrapper: %v", err)
	}
	if err != nil {
		t.Fatalf("net.FilePacketConn: %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close conn: %v", err)
		}
	}()

	payload := []byte("datagram")
	if _, err := conn.WriteTo(payload, conn.LocalAddr()); err != nil {
		t.Fatalf("write to self: %v", err)
	}
	buf := make([]byte, len(payload))
	n, _, err := conn.ReadFrom(buf)
	if err != nil {
		t.Fatalf("read from self: %v", err)
	}
	if got := string(buf[:n]); got != string(payload) {
		t.Errorf("read %q, want %q", got, payload)
	}
}

func TestCloseSocketClosesDescriptor(t *testing.T) {
	fd, err := newDatagramSocket(false)
	if err != nil {
		t.Fatalf("newDatagramSocket(false): %v", err)
	}

	closeSocket(fd, "test")

	if err := syscall.Close(fd); !errors.Is(err, syscall.EBADF) {
		t.Errorf("second close returned %v, want EBADF — the descriptor was not closed", err)
	}
}

// TestCloseSocketSurvivesBadDescriptor pins the contract that closeSocket
// swallows its error: it runs on a path that is already returning a different
// error, so it must never panic and has nothing to report.
func TestCloseSocketSurvivesBadDescriptor(t *testing.T) {
	closeSocket(-1, "deliberately invalid")
}

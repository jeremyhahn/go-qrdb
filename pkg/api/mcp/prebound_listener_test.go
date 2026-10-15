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

package mcp

import (
	"errors"
	"net"
	"testing"
)

// TestWithListener_ServesOnTheHandedOverSocket pins go-qrdb#693 for MCP: the
// server serves on the listener it was handed and never binds its configured
// address, so the sealed-window handover can keep the port held.
func TestWithListener_ServesOnTheHandedOverSocket(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	// A configured address that cannot bind proves it is not used.
	srv := NewServer(newMockKVService(), newMockAdminService(), "256.0.0.1:1",
		WithTLS(testTLSConfig(t)), WithListener(ln))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)

	if got, want := srv.Addr(), ln.Addr().String(); got != want {
		t.Fatalf("Addr() = %q, want the handed-over listener %q", got, want)
	}
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial the served listener: %v", err)
	}
	_ = conn.Close()
}

// TestWithListener_RefusedStartClosesIt pins the ownership contract: a Start
// that refuses to serve still owns the handed listener and closes it, rather
// than leaking a bound port nobody will ever serve or release.
func TestWithListener_RefusedStartClosesIt(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0", WithListener(ln))
	err = srv.Start()
	var want *TLSNotConfiguredError
	if !errors.As(err, &want) {
		t.Fatalf("Start = %v, want *TLSNotConfiguredError", err)
	}
	if _, err := ln.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("listener still open after a refused Start (Accept err = %v)", err)
	}
}

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

package grpc

import (
	"errors"
	"net"
	"testing"
)

// TestWithListener_ServesOnTheHandedOverSocket pins go-qrdb#374 for gRPC: the
// server serves on the listener it was given, not on its configured address.
func TestWithListener_ServesOnTheHandedOverSocket(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	// A configured address that cannot bind proves it is not used.
	srv := NewServer(newMockKVService(), newMockAdminService(), "256.0.0.1:1",
		WithTLS(testServerTLSConfig(t)), WithListener(ln))
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

// TestWithListener_RefusedStartClosesIt pins ownership: a Start that refuses
// before serving closes the listener it was handed rather than leaking it.
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

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

// Dual-stack test port allocation (go-qrdb#248).
//
// The REST listener takes a TCP port and the HTTP/3 listener then binds THE
// SAME NUMBER ON UDP. TCP and UDP are separate port namespaces, so a port the
// kernel hands out as a free TCP port says nothing about whether UDP is free
// on it. Asking for AdminPort: 0 therefore races: the kernel gives us TCP port
// P, another test already holds UDP P, and the HTTP/3 bind fails.
//
// This file used to carry its own allocator for that: kernel-drawn ":0"
// candidates, each checked on both stacks and released. That fixed the
// protocol half and kept the other two hazards -- the number was inside the
// ephemeral range, so any outbound socket could take it between the probe and
// the bind, and nothing stopped a concurrent test PROCESS from certifying the
// same number. The shared harness allocator closes all three (window below
// the ephemeral range, dual-stack probe, cross-process lock), so this package
// takes its ports from there instead of keeping a weaker copy.
package server

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/internal/netport"
)

// freeDualStackPort returns a port free on both TCP and UDP, for a test that
// starts a REST listener whose HTTP/3 sibling shares the port number.
func freeDualStackPort(t *testing.T) int {
	t.Helper()
	ports, err := netport.Reserve("127.0.0.1", 1)
	if err != nil {
		t.Fatalf("freeDualStackPort: %v", err)
	}
	port := ports[0]
	return port
}

// TestStartRESTWithHTTP3_OnDualStackPort is the end-to-end form: a REST server
// started on a port chosen this way brings up both the TCP listener and its
// HTTP/3 sibling. With a TCP-only port pick this is the call that failed in CI.
func TestStartRESTWithHTTP3_OnDualStackPort(t *testing.T) {
	cfg := testConfigFull(t, protocolFlags{rest: true}, "")
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithTLS(selfSignedTLSConfig(t)))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	if srv.RESTAddr() == "" {
		t.Fatal("REST listener reported no address")
	}
}

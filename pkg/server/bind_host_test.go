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

// Closes the last link of the bind-host chain for go-qrdb#167: config.Server.Host
// -> the address each protocol listener actually bound. The daemon-side half
// (a value set by file/env/flag reaching this struct field) is asserted in
// cmd/qrdbd/bind_host_test.go.
package server

import (
	"net"
	"testing"
)

// TestStart_EveryListenerBindsConfiguredHost starts the multi-protocol
// orchestrator with a specific bind host and asserts every listener it opened
// landed there. Port 0 keeps the test cheap and independent of what else is
// running; the host is what is under test.
func TestStart_EveryListenerBindsConfiguredHost(t *testing.T) {
	cfg := testConfigFull(t, protocolFlags{rest: true, grpc: true, quic: true, mcp: true}, "")
	cfg.Server.Host = "127.0.0.1"

	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	listeners := map[string]string{
		"REST": srv.RESTAddr(),
		"gRPC": srv.GRPCAddr(),
		"QUIC": srv.QUICAddr(),
		"MCP":  srv.MCPAddr(),
	}
	bound := 0
	for name, addr := range listeners {
		if addr == "" {
			continue // protocol not running in this build
		}
		bound++
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			t.Fatalf("%s listener addr %q: %v", name, addr, err)
		}
		if host != "127.0.0.1" {
			t.Errorf("%s listener bound %q, want host 127.0.0.1: server.host did not reach this listener", name, addr)
		}
		if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
			t.Errorf("%s listener bound the wildcard host %q while server.host was 127.0.0.1", name, addr)
		}
	}
	if bound == 0 {
		t.Fatal("no listener reported an address; this test asserts nothing as written")
	}
}

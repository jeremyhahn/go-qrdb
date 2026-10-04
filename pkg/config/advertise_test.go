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

package config

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
)

// TestAdvertisedAPIEndpoints_ServedProtocolsOnTheAdvertiseHost: every served
// protocol is published on the advertise host with its own port; a disabled
// one is not.
func TestAdvertisedAPIEndpoints_ServedProtocolsOnTheAdvertiseHost(t *testing.T) {
	c := &Config{
		Server:    ServerConfig{AdvertiseHost: "db1.example.com", AdminPort: 63002, GRPCPort: 63003, QUICPort: 63004},
		Protocols: ProtocolsConfig{REST: true, GRPC: true, QUIC: true, MCP: false},
		MCP:       MCPConfig{Port: 63005},
		Raft:      RaftConfig{RaftAddress: "192.0.2.1:63001"},
	}
	got, err := c.AdvertisedAPIEndpoints()
	if err != nil {
		t.Fatal(err)
	}
	want := map[cluster.APIProtocol]string{
		cluster.APIProtocolREST: "db1.example.com:63002",
		cluster.APIProtocolGRPC: "db1.example.com:63003",
		cluster.APIProtocolQUIC: "db1.example.com:63004",
	}
	if len(got) != len(want) {
		t.Fatalf("endpoints = %v; want %v", got, want)
	}
	for p, addr := range want {
		if got[p] != addr {
			t.Errorf("%s = %q; want %q", p, got[p], addr)
		}
	}
}

// TestAdvertisedAPIEndpoints_FallsBackToTheRaftHost: with no advertise host
// the Raft address's host is used, and a wildcard there publishes nothing.
func TestAdvertisedAPIEndpoints_FallsBackToTheRaftHost(t *testing.T) {
	c := &Config{
		Server:    ServerConfig{GRPCPort: 63003},
		Protocols: ProtocolsConfig{GRPC: true, MCP: true},
		MCP:       MCPConfig{Port: 63005},
		Raft:      RaftConfig{RaftAddress: "192.0.2.7:63001"},
	}
	got, err := c.AdvertisedAPIEndpoints()
	if err != nil || got[cluster.APIProtocolGRPC] != "192.0.2.7:63003" || got[cluster.APIProtocolMCP] != "192.0.2.7:63005" {
		t.Fatalf("endpoints = %v, %v; want gRPC and MCP on the Raft host", got, err)
	}
	c.Raft.RaftAddress = "0.0.0.0:63001"
	if got, err := c.AdvertisedAPIEndpoints(); err != nil || got != nil {
		t.Fatalf("a wildcard host published %v, %v; want nothing", got, err)
	}
}

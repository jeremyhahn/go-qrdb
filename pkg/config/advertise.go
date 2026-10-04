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

import "github.com/jeremyhahn/go-qrdb/pkg/cluster"

// AdvertisedAPIEndpoints returns the API endpoints this node publishes in its
// member record: the advertise host (Server.AdvertiseHost, else the host of
// Raft.RaftAddress -- the address peers already dial) joined with the port of
// every client-facing protocol this node serves. A disabled protocol is
// omitted, and a host no peer can dial (a wildcard) publishes nothing rather
// than an address that cannot be reached. The derivation lives here so the
// daemon and the embeddable node publish the same record from the same
// configuration.
func (c *Config) AdvertisedAPIEndpoints() (map[cluster.APIProtocol]string, error) {
	host := c.Server.AdvertiseHost
	if host == "" {
		host = cluster.HostFromAddress(c.Raft.RaftAddress)
	}
	served := []struct {
		proto   cluster.APIProtocol
		enabled bool
		port    int
	}{
		{cluster.APIProtocolREST, c.Protocols.REST, c.Server.AdminPort},
		{cluster.APIProtocolGRPC, c.Protocols.GRPC, c.Server.GRPCPort},
		{cluster.APIProtocolQUIC, c.Protocols.QUIC, c.Server.QUICPort},
		{cluster.APIProtocolMCP, c.Protocols.MCP, c.MCP.Port},
	}
	ports := make(map[cluster.APIProtocol]int, len(served))
	for _, p := range served {
		if p.enabled {
			ports[p.proto] = p.port
		}
	}
	return cluster.BuildAPIEndpoints(host, ports)
}

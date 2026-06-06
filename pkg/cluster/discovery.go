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

package cluster

import (
	"strconv"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/discovery"
)

// Options is the transport-neutral input for discovery-based cluster
// bootstrap. Callers — the qrdbd daemon and embedders such as go-xkms or
// go-quicbaas — translate their own CLI flags or YAML configuration into an
// Options value, keeping pkg/cluster free of any flag or config coupling.
type Options struct {
	// Method selects the discovery mechanism: "static", "multicast",
	// "dns", or "token".
	Method string

	// NodeID is this node's unique identifier (its ReplicaID within the
	// shard) used when announcing itself to peers.
	NodeID uint64

	// RaftAddress is this node's Raft network address to announce.
	RaftAddress string

	// MinPeers is the minimum cluster size used for quorum. Zero means the
	// package default (defaultMinPeers).
	MinPeers int

	// Peers holds "id=address" specifications for static discovery.
	Peers []string

	// MulticastSecret is the HMAC-SHA256 shared secret for multicast
	// discovery. Optional; when empty the daemon rejects multicast, but
	// embedders may run unauthenticated multicast on trusted networks.
	MulticastSecret string

	// DeploymentID isolates multicast discovery to a specific deployment.
	DeploymentID uint64

	// MulticastGroup overrides the default multicast address. Optional.
	MulticastGroup string

	// DNSDomain is the domain queried for DNS SRV discovery.
	DNSDomain string

	// DNSService is the SRV service label (default "_raft").
	DNSService string

	// DNSProto is the SRV protocol label (default "_udp").
	DNSProto string

	// TokenEndpoint is the URL of the token discovery service.
	TokenEndpoint string

	// Token is the shared bootstrap token sent as a bearer credential.
	Token string

	// TokenClusterSize is the expected number of nodes token discovery
	// waits for before returning.
	TokenClusterSize int

	// AllowInsecureTokenHTTP permits http:// token endpoints for
	// development and testing. Production endpoints must use https://.
	AllowInsecureTokenHTTP bool
}

// discoveryBuilder constructs a discovery.Method from Options. Each method
// has its own builder; the set is registered in discoveryBuilders.
type discoveryBuilder func(Options) (discovery.Method, error)

// discoveryBuilders maps discovery method names to their builder functions.
// This map is the single source of truth for which methods qrdb supports;
// adding a method here makes it available to every caller (daemon and
// embedders) without further wiring.
var discoveryBuilders = map[string]discoveryBuilder{
	"static":    buildStaticDiscovery,
	"multicast": buildMulticastDiscovery,
	"dns":       buildDNSDiscovery,
	"token":     buildTokenDiscovery,
}

// BuildDiscovery constructs the discovery.Method named by opts.Method.
// It returns UnknownDiscoveryError for an unregistered method name and a
// method-specific error when required inputs are missing or invalid.
func BuildDiscovery(opts Options) (discovery.Method, error) {
	builder, ok := discoveryBuilders[opts.Method]
	if !ok {
		return nil, &UnknownDiscoveryError{Name: opts.Method}
	}
	return builder(opts)
}

// buildStaticDiscovery creates a StaticDiscovery from the parsed peers.
func buildStaticDiscovery(opts Options) (discovery.Method, error) {
	members, err := ParsePeers(opts.Peers)
	if err != nil {
		return nil, err
	}

	peers := make([]discovery.Peer, 0, len(members))
	for id, addr := range members {
		peers = append(peers, discovery.Peer{NodeID: id, Address: addr})
	}

	return discovery.NewStaticDiscovery(discovery.StaticConfig{
		Peers:        peers,
		SharedSecret: secretBytes(opts.MulticastSecret),
	}), nil
}

// buildMulticastDiscovery creates and starts a MulticastDiscovery.
func buildMulticastDiscovery(opts Options) (discovery.Method, error) {
	if opts.MulticastSecret == "" {
		return nil, &MissingFlagError{Flag: "multicast-secret"}
	}

	cfg := discovery.MulticastConfig{
		NodeID:         opts.NodeID,
		Address:        opts.RaftAddress,
		DeploymentID:   opts.DeploymentID,
		SharedSecret:   []byte(opts.MulticastSecret),
		MulticastGroup: opts.MulticastGroup,
		MinPeers:       minPeers(opts) - 1, // other peers, excluding self
	}

	md, err := discovery.NewMulticastDiscovery(cfg)
	if err != nil {
		return nil, err
	}
	md.Start()

	return md, nil
}

// buildDNSDiscovery creates a DNSDiscovery from the SRV options.
func buildDNSDiscovery(opts Options) (discovery.Method, error) {
	if opts.DNSDomain == "" {
		return nil, &MissingFlagError{Flag: "dns-domain"}
	}

	service := opts.DNSService
	if service == "" {
		service = "_raft"
	}
	proto := opts.DNSProto
	if proto == "" {
		proto = "_udp"
	}

	return discovery.NewDNSDiscovery(discovery.DNSConfig{
		Service: service,
		Proto:   proto,
		Domain:  opts.DNSDomain,
	}), nil
}

// buildTokenDiscovery creates a TokenDiscovery from the token options.
func buildTokenDiscovery(opts Options) (discovery.Method, error) {
	if opts.TokenEndpoint == "" {
		return nil, &MissingFlagError{Flag: "discovery-endpoint"}
	}
	if opts.Token == "" {
		return nil, &MissingFlagError{Flag: "discovery-token"}
	}

	size := opts.TokenClusterSize
	if size < 1 {
		size = minPeers(opts)
	}

	return discovery.NewTokenDiscovery(discovery.TokenConfig{
		Endpoint:          opts.TokenEndpoint,
		Token:             opts.Token,
		NodeID:            opts.NodeID,
		Address:           opts.RaftAddress,
		ClusterSize:       size,
		AllowInsecureHTTP: opts.AllowInsecureTokenHTTP,
	})
}

// ParsePeers parses a slice of "id=address" strings into a member map.
// It returns PeerParseError for any malformed specification.
func ParsePeers(peers []string) (map[uint64]string, error) {
	members := make(map[uint64]string, len(peers))
	for _, p := range peers {
		idx := strings.IndexByte(p, '=')
		if idx < 1 {
			return nil, &PeerParseError{Input: p}
		}

		id, err := strconv.ParseUint(p[:idx], 10, 64)
		if err != nil {
			return nil, &PeerParseError{Input: p}
		}

		addr := p[idx+1:]
		if addr == "" {
			return nil, &PeerParseError{Input: p}
		}

		members[id] = addr
	}
	return members, nil
}

// IsSingleNode reports whether the options describe a single-node cluster
// (only the local node). Non-static discovery methods always assume
// multi-node, since they discover peers at runtime.
func IsSingleNode(opts Options) bool {
	if opts.Method != "static" {
		return false
	}
	if len(opts.Peers) == 0 {
		return true
	}
	if len(opts.Peers) == 1 {
		members, err := ParsePeers(opts.Peers)
		if err != nil {
			return false
		}
		return len(members) == 1
	}
	return false
}

// minPeers returns the effective minimum cluster size for opts, applying
// the package default when unset.
func minPeers(opts Options) int {
	if opts.MinPeers > 0 {
		return opts.MinPeers
	}
	return defaultMinPeers
}

// secretBytes returns the byte form of a shared secret, or nil when empty,
// so a zero-value secret does not allocate an empty non-nil slice.
func secretBytes(s string) []byte {
	if s == "" {
		return nil
	}
	return []byte(s)
}

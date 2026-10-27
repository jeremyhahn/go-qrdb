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
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/discovery"
)

// ---------------------------------------------------------------------------
// Runtime discovery primitives
//
// These aliases re-export the canonical peer-discovery implementation owned by
// github.com/jeremyhahn/go-qrdb/pkg/discovery. They are the runtime backends
// (interfaces, config structs, constructors) used to actually find peers — as
// opposed to the YAML/config-layer discovery aliases in types_database.go
// (database.DiscoveryMethod et al.), which describe how discovery is requested.
//
// To avoid clashing with that pre-existing config layer in the flat sdk
// package, the runtime types carry a Discovery prefix.
// ---------------------------------------------------------------------------

// Peer is a discovered cluster peer.
type Peer = discovery.Peer

// DiscoveryBackend is the behavior implemented by every runtime discovery
// method (discovery.Method).
type DiscoveryBackend = discovery.Method

// DiscoveryConfig is the shared runtime discovery configuration.
type DiscoveryConfig = discovery.Config

// DiscoveryStaticConfig configures static (fixed peer-list) discovery.
type DiscoveryStaticConfig = discovery.StaticConfig

// DiscoveryMulticastConfig configures multicast (LAN announcement) discovery.
type DiscoveryMulticastConfig = discovery.MulticastConfig

// DiscoveryDNSConfig configures DNS SRV-record discovery.
type DiscoveryDNSConfig = discovery.DNSConfig

// DiscoveryTokenConfig configures token-server (HTTP) discovery.
type DiscoveryTokenConfig = discovery.TokenConfig

// DiscoveryManager fans out across multiple discovery methods.
type DiscoveryManager = discovery.Manager

// DiscoveryManagerConfig configures a DiscoveryManager.
type DiscoveryManagerConfig = discovery.ManagerConfig

// Runtime discovery constructors.
var (
	// NewDiscoveryManager builds a discovery Manager from its config.
	NewDiscoveryManager = discovery.NewManager

	// NewStaticDiscovery builds a static discovery method.
	NewStaticDiscovery = discovery.NewStaticDiscovery

	// NewMulticastDiscovery builds a multicast discovery method.
	NewMulticastDiscovery = discovery.NewMulticastDiscovery

	// NewDNSDiscovery builds a DNS SRV discovery method.
	NewDNSDiscovery = discovery.NewDNSDiscovery

	// NewTokenDiscovery builds a token-server discovery method.
	NewTokenDiscovery = discovery.NewTokenDiscovery
)

// ---------------------------------------------------------------------------
// Cluster-formation entry points
//
// Re-exported from github.com/jeremyhahn/go-qrdb/pkg/cluster.
// ---------------------------------------------------------------------------

// DiscoveryOptions configures cluster-formation discovery (BuildDiscovery).
type DiscoveryOptions = cluster.Options

// Cluster-formation constructors.
var (
	// BuildDiscovery constructs a DiscoveryBackend from DiscoveryOptions.
	BuildDiscovery = cluster.BuildDiscovery

	// BootstrapShard forms or joins a Raft shard using peer discovery.
	BootstrapShard = cluster.BootstrapShard

	// ParsePeers parses a slice of "id=address" strings into a member map
	// suitable for Raft peer configuration. Returns an error when any entry
	// is malformed. (Aliases pkg/cluster.ParsePeers.)
	ParsePeers = cluster.ParsePeers
)

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

package lifecycle

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterkeys"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// keySenderTimeout bounds one key delivery to a peer.
const keySenderTimeout = 15 * time.Second

// PeerAdminRegistry resolves a cluster node's admin HTTPS base URL from its
// Raft address. The cluster key plane's sender and REST's leader forwarding
// both resolve peers through it.
type PeerAdminRegistry interface {
	rest.LeaderAdminResolver
	// Register records adminURL as the admin base URL of the node at
	// raftAddr.
	Register(raftAddr, adminURL string)
}

// PeerAdminConfig is what a node's peer admin registry is built from.
type PeerAdminConfig struct {
	// RaftAddr is this node's Raft address: the key peers resolve it by.
	RaftAddr string
	// AdvertiseHost is the host peers reach this node on; empty means the
	// host of RaftAddr.
	AdvertiseHost string
	// AdminPort is this node's admin (REST) port, and the port the registry
	// assumes for a peer it has no entry for.
	AdminPort int
	// Peers maps each peer's Raft address to its admin base URL, for nodes
	// that share a host on distinct admin ports (PeerAdminURLMap).
	Peers map[string]string
	// Logger receives the warning for a node with no advertisable host.
	Logger *slog.Logger
}

// NewPeerAdminRegistry returns the registry: this node under the URL a peer
// reaches it on (SelfAdminURL), then every configured peer. A peer with no
// entry resolves to its Raft host on the local admin port -- right for a
// cluster where every node runs the same admin port on its own host.
func NewPeerAdminRegistry(c PeerAdminConfig) PeerAdminRegistry {
	logger := c.Logger
	if logger == nil {
		logger = slog.Default()
	}
	registry := rest.NewPeerAdminRegistryTLS(strconv.Itoa(c.AdminPort))
	// The entry is keyed by the Raft address -- the identity peers use -- so
	// its value must be the URL a PEER would reach this node on: the
	// advertise host. A loopback literal published "reach me at 127.0.0.1"
	// into the shared registry (go-qrdb#167). A node whose advertise host is
	// a wildcard has no such URL, so it registers nothing rather than a wrong
	// one.
	if self, ok := SelfAdminURL(c.RaftAddr, c.AdvertiseHost, c.AdminPort); ok {
		registry.Register(c.RaftAddr, self)
	} else {
		logger.Warn("no advertisable host for this node's admin URL; skipping self-registration in the peer registry",
			"raft_address", c.RaftAddr,
			"advertise_host", c.AdvertiseHost)
	}
	for raftAddr, adminURL := range c.Peers {
		if raftAddr == c.RaftAddr {
			continue
		}
		registry.Register(raftAddr, adminURL)
	}
	return registry
}

// SelfAdminURL returns the admin HTTPS base URL a peer reaches this node on:
// the advertise host, else the host of the Raft address, on the admin port.
// It reports false when that host is not advertisable (a wildcard).
func SelfAdminURL(raftAddr, advertiseHost string, adminPort int) (string, bool) {
	host := advertiseHost
	if host == "" {
		host = cluster.HostFromAddress(raftAddr)
	}
	if !cluster.AdvertisableHost(host) {
		return "", false
	}
	return "https://" + net.JoinHostPort(host, strconv.Itoa(adminPort)), true
}

// PeerAdminURLMap returns auth.peer_admin_urls as a map from Raft address to
// admin URL, dropping an entry that lacks either; nil when none remains.
func PeerAdminURLMap(entries []qrdbconfig.PeerAdminEntry) map[string]string {
	var m map[string]string
	for _, e := range entries {
		if e.RaftAddr == "" || e.AdminURL == "" {
			continue
		}
		if m == nil {
			m = make(map[string]string, len(entries))
		}
		m[e.RaftAddr] = e.AdminURL
	}
	return m
}

// RaftTenantClusterConfig is what the multi-node half of raft tenant
// isolation is composed from on a clustered node: qrdbd and an embedded
// clustered node (sdk/go/node) compose it identically.
type RaftTenantClusterConfig struct {
	// Services are the node's services; their DatabaseService and
	// RebalanceService receive the key distributor and the replica host.
	Services *Node
	// Host is the shared Raft host dedicated raft groups run on; nil on a
	// node without one, which composes nothing that needs it.
	Host *quicraft.Host
	// FeatureGate admits a replica only on a node advertising every feature
	// its group uses.
	FeatureGate *featuregate.Gate
	// Barriers is the node's barrier registry: the tenant keys the key plane
	// sends and receives.
	Barriers *seal.BarrierRegistry
	// DedicatedRaft reports that tenant isolation built the IsolationRaft
	// backend factory, so dedicated raft groups run on Host.
	DedicatedRaft bool
	// RaftAddr is this node's Raft address, its identity in placements.
	RaftAddr string
	// PeerAdmin resolves the admin URL a peer's key routes are served on.
	PeerAdmin PeerAdminRegistry
	// TrustDomain is the cluster trust domain every node certificate's SPIFFE
	// SAN, the receiver's peer check and the sender's recipient check share.
	TrustDomain string
	// CACertPool is the cluster CA pool peer certificates verify to.
	CACertPool *x509.CertPool
	// CertPEM and KeyPEM are this node's certificate and EC key: its mTLS
	// client identity to peers and its HPKE recipient key.
	CertPEM, KeyPEM []byte
	// Revocation is the authentication middleware's revocation checker, so
	// the key plane honours revocation within the same cache window; nil
	// without one.
	Revocation auth.RevocationChecker
	// Logger receives the key plane's warnings.
	Logger *slog.Logger
}

// RaftTenantCluster is the composed multi-node half of raft tenant
// isolation.
type RaftTenantCluster struct {
	// KeyReceiver serves /v1/cluster/nodekey and /v1/cluster/keydelivery; nil
	// when the key plane is not composed.
	KeyReceiver *clusterkeys.Receiver
	// KeySender delivers a tenant's key to the nodes its placement names; nil
	// when the key plane is not composed.
	KeySender *clusterkeys.Sender
}

// ServerOptions returns the server options the composition needs: the key
// receiver's routes.
func (c *RaftTenantCluster) ServerOptions() []server.ServerOption {
	if c == nil || c.KeyReceiver == nil {
		return nil
	}
	return []server.ServerOption{server.WithClusterKeyReceiver(c.KeyReceiver)}
}

// ComposeRaftTenantCluster composes the multi-node half of raft tenant
// isolation (Phases 4c-1c, 4c-1d, 4c-2):
//
//   - the cluster key plane: an HPKE receiver for the tenant keys peers
//     deliver, and a sender delivering this node's to the replicas a
//     placement names -- only with a Raft host, a barrier registry and a
//     placement table, and a usable node key;
//   - DatabaseService's key distributor, so a dedicated raft database's
//     tenant key reaches its replicas when it is placed;
//   - RebalanceService's placement table, key deliverer and barrier lookup,
//     for a dedicated tenant group's MOVE;
//   - with dedicated raft groups, the replica host both services add and
//     remove replicas through.
//
// A missing piece disables what needs it, with a warning, rather than
// failing the boot: a single-node or passphrase-sealed deployment needs none
// of it.
func ComposeRaftTenantCluster(c RaftTenantClusterConfig) *RaftTenantCluster {
	logger := c.Logger
	if logger == nil {
		logger = slog.Default()
	}
	out := &RaftTenantCluster{}
	if c.Host != nil && c.Barriers != nil && c.Services != nil && c.Services.PlacementStore != nil {
		out.KeyReceiver = composeKeyReceiver(c, logger)
		if out.KeyReceiver != nil {
			out.KeySender = composeKeySender(c, logger)
		}
	}
	if c.Services == nil {
		return out
	}
	if out.KeySender != nil {
		if concrete, ok := c.Services.Database.(*service.DatabaseService); ok {
			concrete.SetDistributor(out.KeySender, c.Barriers, c.RaftAddr)
		}
	}
	// Every dependency is nil-guarded inside: without the sender or the
	// placement table the dedicated-tenant MOVE path is off and the system
	// shard path is used.
	if concrete, ok := c.Services.Rebalance.(*service.RebalanceService); ok {
		var deliverer service.RebalanceKeyDeliverer
		if out.KeySender != nil {
			deliverer = out.KeySender
		}
		var lookup service.RebalanceBarrierLookup
		if c.Barriers != nil {
			lookup = c.Barriers
		}
		concrete.SetRebalanceDeps(c.Services.PlacementStore, deliverer, lookup, c.RaftAddr)
	}
	if c.DedicatedRaft && c.Host != nil {
		replicas := &replicaHost{host: c.Host, gate: c.FeatureGate}
		if concrete, ok := c.Services.Database.(*service.DatabaseService); ok {
			concrete.SetRaftHost(replicas)
		}
		if concrete, ok := c.Services.Rebalance.(*service.RebalanceService); ok {
			concrete.SetRaftHost(replicas)
		}
	}
	return out
}

// composeKeyReceiver builds the HPKE receiver from the node's EC key, or
// returns nil -- the key plane is then off -- when the key does not parse or
// a constructor refuses.
func composeKeyReceiver(c RaftTenantClusterConfig, logger *slog.Logger) *clusterkeys.Receiver {
	nodePriv := nodeECKey(c.KeyPEM, logger)
	if nodePriv == nil {
		return nil
	}
	kd, err := keydelivery.NewReceiver(nodePriv, c.RaftAddr)
	if err != nil {
		logger.Warn("clusterkeys: key delivery receiver init failed, peer key transport disabled", slog.Any("error", err))
		return nil
	}
	rcv, err := clusterkeys.NewReceiver(kd, c.Barriers, c.Services.PlacementStore, c.TrustDomain, c.RaftAddr,
		c.CACertPool, c.Revocation, c.CertPEM, logger)
	if err != nil {
		logger.Warn("clusterkeys: cluster key receiver init failed, peer key transport disabled", slog.Any("error", err))
		return nil
	}
	return rcv
}

// composeKeySender builds the sender: an mTLS client presenting the node's
// certificate and trusting the cluster CA, resolving peers through the peer
// admin registry.
func composeKeySender(c RaftTenantClusterConfig, logger *slog.Logger) *clusterkeys.Sender {
	nodeCert, err := tls.X509KeyPair(c.CertPEM, c.KeyPEM)
	if err != nil {
		logger.Warn("clusterkeys: failed to build mTLS client cert, sender disabled", slog.Any("error", err))
		return nil
	}
	client := &http.Client{
		Timeout: keySenderTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				Certificates: []tls.Certificate{nodeCert},
				RootCAs:      c.CACertPool,
				MinVersion:   tls.VersionTLS13,
			},
			ForceAttemptHTTP2: true,
		},
	}
	var resolver clusterkeys.AdminURLResolver
	if c.PeerAdmin != nil {
		resolver = c.PeerAdmin
	}
	sender, err := clusterkeys.NewSender(resolver, client, c.CACertPool, c.TrustDomain, c.Revocation, logger)
	if err != nil {
		logger.Warn("clusterkeys: sender init failed, peer key distribution disabled", slog.Any("error", err))
		return nil
	}
	return sender
}

// nodeECKey parses the node's ECDSA P-256 private key: the key its
// certificate was issued for, and so its HPKE recipient key (DHKEM-P-256).
// A key that does not parse disables the key plane, with a warning.
func nodeECKey(keyPEM []byte, logger *slog.Logger) *ecdsa.PrivateKey {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		logger.Warn("clusterkeys: no node private key, peer key transport disabled")
		return nil
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		logger.Warn("clusterkeys: failed to parse node private key, peer key transport disabled", slog.Any("error", err))
		return nil
	}
	return key
}

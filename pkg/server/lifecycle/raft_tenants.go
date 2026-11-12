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
	"bytes"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"log/slog"
	"net"
	"strconv"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/clock"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterkeys"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// PeerAdminRegistry resolves a cluster node's admin HTTPS base URL from its
// Raft address. REST's leader forwarding resolves peers through it.
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
	// RebalanceService receive the local node ID and the replica host.
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
	// TrustDomain is the cluster trust domain every node certificate's SPIFFE
	// SAN and the receiver's peer check share.
	TrustDomain string
	// CACertPool is the cluster CA pool peer certificates verify to.
	CACertPool *x509.CertPool
	// Identity is this node's mTLS identity: its certificate chain, leaf
	// first, and the crypto.Signer for the leaf's key. It is the
	// certificate the receiver publishes for peers to seal deliveries to. A composer whose
	// node key is held on a device sets PrivateKey to a signer that signs
	// there; no key bytes are needed. Nil disables the key plane.
	Identity *tls.Certificate
	// Decrypter opens the tenant keys peers deliver, which are sealed to the
	// public key of Identity's leaf (DHKEM-P-256). A composer whose node key
	// is held on a device builds it with keydelivery.NewDecrypter, so the key
	// agreement runs on the device; nil derives the software default from an
	// *ecdsa.PrivateKey in Identity, and with any other key the key plane is
	// off. A Decrypter for any other key disables the key plane.
	Decrypter keydelivery.Decrypter
	// Revocation is the authentication middleware's revocation checker, so
	// the key plane honours revocation within the same cache window; nil
	// without one.
	Revocation auth.RevocationChecker
	// Logger receives the key plane's warnings.
	Logger *slog.Logger
	// Clock is the time source of the key-delivery freshness window and
	// replay guard; nil is the real clock.
	Clock clock.Clock
}

// RaftTenantCluster is the composed multi-node half of raft tenant
// isolation.
type RaftTenantCluster struct {
	// KeyReceiver serves /v1/cluster/nodekey and /v1/cluster/keydelivery; nil
	// when the key plane is not composed.
	KeyReceiver *clusterkeys.Receiver
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
//   - the cluster key plane: the node-key route and an HPKE delivery
//     receiver, which refuses every delivery mode until one is registered
//     (no tenant key is shipped between nodes) -- only with a Raft host, a
//     barrier registry and a placement table, and a usable node key;
//   - the platform's init statement for a tenant that chose platform custody:
//     signed with the node's key under its certificate when such a tenant is
//     created here, and trusted through CACertPool, the CA node certificates
//     chain to, for that tenant alone (wirePlatformInit) -- so every node it
//     is placed on initializes its own key under its own system barrier;
//   - DatabaseService's and RebalanceService's local node ID, and
//     RebalanceService's placement table for a dedicated tenant group's MOVE.
//     No tenant key is shipped: a replica placed later (scale-out, MOVE)
//     keys itself from its owner's statement on the init watcher's next poll;
//   - with dedicated raft groups, the replica host both services add and
//     remove replicas through, and the barrier init watcher: every tenant
//     barrier a group placed here belongs to is initialized with this node's
//     own key from its owner's claimed statement (startBarrierInitWatcher).
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
	}
	if c.Services == nil {
		return out
	}
	wireBarrierNodes(c.Services)
	if c.Host != nil {
		wirePlatformInit(c.Services, c.CACertPool, c.Identity)
	}
	if concrete, ok := c.Services.Database.(*service.DatabaseService); ok {
		concrete.SetLocalNodeID(c.RaftAddr)
	}
	// Without the placement table the dedicated-tenant MOVE path is off and
	// the system shard path is used.
	if concrete, ok := c.Services.Rebalance.(*service.RebalanceService); ok {
		concrete.SetRebalanceDeps(c.Services.PlacementStore, c.RaftAddr)
	}
	if c.DedicatedRaft && c.Host != nil {
		replicas := &replicaHost{host: c.Host, gate: c.FeatureGate}
		if concrete, ok := c.Services.Database.(*service.DatabaseService); ok {
			concrete.SetRaftHost(replicas)
		}
		if concrete, ok := c.Services.Rebalance.(*service.RebalanceService); ok {
			concrete.SetRaftHost(replicas)
		}
		if err := startBarrierInitWatcher(c.Services, c.RaftAddr, logger); err != nil {
			logger.Warn("barrier init: watcher not started; this node initializes no tenant barrier from its owner's statement",
				slog.Any("error", err))
		}
	}
	return out
}

// composeKeyReceiver builds the HPKE receiver from the node's identity and
// decrypter, or returns nil -- the key plane is then off -- when either is
// missing, the decrypter does not open what peers seal to the node's
// certificate, or a constructor refuses.
func composeKeyReceiver(c RaftTenantClusterConfig, logger *slog.Logger) *clusterkeys.Receiver {
	leaf, err := identityLeaf(c.Identity)
	if err != nil {
		logger.Warn("clusterkeys: no usable node identity, peer key transport disabled", slog.Any("error", err))
		return nil
	}
	dec := c.Decrypter
	if dec == nil {
		dec = nodeDecrypter(c.Identity.PrivateKey, logger)
		if dec == nil {
			return nil
		}
	}
	if err := checkNodeDecrypter(dec, leaf); err != nil {
		logger.Warn("clusterkeys: node decrypter does not match the node certificate, peer key transport disabled", slog.Any("error", err))
		return nil
	}
	kd, err := keydelivery.NewReceiver(dec, c.RaftAddr, keydelivery.WithClock(c.Clock))
	if err != nil {
		logger.Warn("clusterkeys: key delivery receiver init failed, peer key transport disabled", slog.Any("error", err))
		return nil
	}
	rcv, err := clusterkeys.NewReceiver(kd, c.Services.PlacementStore, c.TrustDomain, c.RaftAddr,
		c.CACertPool, c.Revocation, identityPEM(c.Identity), logger)
	if err != nil {
		logger.Warn("clusterkeys: cluster key receiver init failed, peer key transport disabled", slog.Any("error", err))
		return nil
	}
	return rcv
}

// NodeIdentity is the Identity of RaftTenantClusterConfig, derived from the
// cluster mTLS material the node's Raft transport is configured with: the one
// derivation cmd/qrdbd and sdk/go/node both put their node certificate
// through, so the key plane and the platform-init signer read the same
// identity on either. A composer-built Certificate (a key held on a device)
// is used as is; otherwise the PEM certificate and key are parsed as a pair.
// A nil id is no identity, which is not an error; a pair that does not parse
// is a *NodeIdentityError.
func NodeIdentity(id *quicraftconfig.MTLSConfig) (*tls.Certificate, error) {
	if id == nil {
		return nil, nil
	}
	if id.Certificate != nil {
		return id.Certificate, nil
	}
	pair, err := tls.X509KeyPair(id.Cert, id.Key)
	if err != nil {
		return nil, &NodeIdentityError{Reason: "node certificate and key do not form a pair", Err: err}
	}
	return &pair, nil
}

// identityLeaf parses the leaf of the node's identity: the certificate it
// presents to peers and the one they seal tenant keys to.
func identityLeaf(id *tls.Certificate) (*x509.Certificate, error) {
	if id == nil || len(id.Certificate) == 0 {
		return nil, &NodeIdentityError{Reason: "no node certificate"}
	}
	leaf, err := x509.ParseCertificate(id.Certificate[0])
	if err != nil {
		return nil, &NodeIdentityError{Reason: "node certificate does not parse", Err: err}
	}
	return leaf, nil
}

// identityPEM is the node's certificate chain, leaf first, as PEM: what the
// receiver publishes for peers to seal to.
func identityPEM(id *tls.Certificate) []byte {
	var out []byte
	for _, der := range id.Certificate {
		out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	return out
}

// nodeDecrypter is the software default node decrypter: the node's identity
// key -- an ECDSA P-256 key, or one that agrees keys itself -- as a
// DHKEM-P-256 HPKE key. A key held behind a signer that cannot agree keys has
// no default; the key plane is off, with a warning, until the composer
// supplies a Decrypter.
func nodeDecrypter(key crypto.PrivateKey, logger *slog.Logger) keydelivery.Decrypter {
	dec, err := keydelivery.PrivateKeyFor(keydelivery.KEMP256, key)
	if err != nil {
		logger.Warn("clusterkeys: node key cannot open key deliveries and no decrypter was supplied, peer key transport disabled", slog.Any("error", err))
		return nil
	}
	return dec
}

// checkNodeDecrypter verifies dec opens what peers seal to the node: they
// seal to the public key of the certificate the node publishes, so a
// decrypter for any other key would fail every delivery.
func checkNodeDecrypter(dec keydelivery.Decrypter, leaf *x509.Certificate) error {
	name, raw, err := keydelivery.RecipientFromPublicKey(leaf.PublicKey)
	if err != nil {
		return &NodeIdentityError{Reason: "node certificate key has no KEM", Err: err}
	}
	if name != keydelivery.KEMP256 || !bytes.Equal(raw, dec.PublicKey().Bytes()) {
		return &NodeIdentityError{Reason: "decrypter is not for the node certificate's key"}
	}
	return nil
}

// NodeIdentityError reports that a node's mTLS identity or its key-delivery
// decrypter cannot serve the key plane: there is no usable certificate, or
// the decrypter cannot open what peers seal to it.
type NodeIdentityError struct {
	Reason string
	Err    error
}

// Error returns a human-readable description.
func (e *NodeIdentityError) Error() string {
	if e.Err != nil {
		return "lifecycle: node identity: " + e.Reason + ": " + e.Err.Error()
	}
	return "lifecycle: node identity: " + e.Reason
}

// Unwrap returns the cause.
func (e *NodeIdentityError) Unwrap() error { return e.Err }

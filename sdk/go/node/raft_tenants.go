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

package node

import (
	"context"
	"crypto/x509"
	"encoding/pem"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
)

// raftTenantClusterOptions composes the multi-node half of raft tenant
// isolation on a clustered node -- the cluster key plane, the key
// distributor, the rebalance dependencies and the replica host -- through
// the same lifecycle.ComposeRaftTenantCluster qrdbd composes it with, and
// returns the server options it needs. The node's identity to its peers is
// its WithRaftMTLS material; peers' admin URLs come from auth.peer_admin_urls
// and, for the rest, the node's own admin port. A standalone node composes
// nothing.
func (n *Node) raftTenantClusterOptions(svcNode *lifecycle.Node, cfg *qrdbconfig.Config, authMW *middleware.AuthMiddleware, standalone bool) []server.ServerOption {
	host, ok := n.host.(*quicraft.Host)
	if standalone || !ok || host == nil || cfg == nil {
		return nil
	}
	c := lifecycle.RaftTenantClusterConfig{
		Services:      svcNode,
		Host:          host,
		FeatureGate:   n.featureGate,
		Barriers:      n.barrierRegistry.Load(),
		DedicatedRaft: n.raftBackend != nil,
		RaftAddr:      cfg.Raft.RaftAddress,
		PeerAdmin: lifecycle.NewPeerAdminRegistry(lifecycle.PeerAdminConfig{
			RaftAddr:      cfg.Raft.RaftAddress,
			AdvertiseHost: cfg.Server.AdvertiseHost,
			AdminPort:     cfg.Server.AdminPort,
			Peers:         lifecycle.PeerAdminURLMap(cfg.Auth.PeerAdminURLs),
			Logger:        n.logger,
		}),
		TrustDomain: cfg.Auth.EffectiveClusterTrustDomain(),
		Decrypter:   n.keyDecrypter,
		Logger:      n.logger,
		Clock:       n.clock,
	}
	if id := n.clusterIdentity; id != nil {
		identity, err := lifecycle.NodeIdentity(id)
		if err != nil {
			n.logger.Warn("clusterkeys: no usable node identity", "error", err)
		}
		c.Identity = identity
		if pool := x509.NewCertPool(); pool.AppendCertsFromPEM(id.CACert) {
			c.CACertPool = pool
		}
	}
	if authMW != nil {
		c.Revocation = authMW.RevocationChecker()
	}
	return lifecycle.ComposeRaftTenantCluster(c).ServerOptions()
}

// byoTLS reports whether this node runs on operator-supplied cluster TLS: a
// clustered node handed its cluster identity (WithRaftMTLS) and no cluster CA
// key of its own (neither WithClusterCAKeyProvider nor WithClusterCAMaterial)
// -- the embedded counterpart of qrdbd's --tls-ca-file, --tls-cert-file and
// --tls-key-file. Such a node holds no Key A, and the CA its identity was
// issued under is the cluster CA (owner ruling 5).
func (n *Node) byoTLS() bool {
	return n.clusterIdentity != nil && n.clusterCAKeyProvider == nil && len(n.clusterCACertPEM) == 0
}

// identityCertPEM is the PEM of the node's cluster leaf certificate: the
// leaf of RaftMTLSConfig.Certificate when set, else Cert; nil without an
// identity.
func identityCertPEM(id *RaftMTLSConfig) []byte {
	switch {
	case id == nil:
		return nil
	case id.Certificate != nil && len(id.Certificate.Certificate) > 0:
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: id.Certificate.Certificate[0]})
	default:
		return id.Cert
	}
}

// startClusterJoin composes the node's member-side CSR join through
// lifecycle.StartClusterJoin, once, on a clustered node.
func (n *Node) startClusterJoin(ctx context.Context, svcNode *lifecycle.Node, cfg *qrdbconfig.Config, standalone bool) error {
	if standalone || cfg == nil || n.clusterJoin != nil {
		return nil
	}
	certPEM := identityCertPEM(n.clusterIdentity)
	cj, err := lifecycle.StartClusterJoin(ctx, svcNode, lifecycle.ClusterJoinConfig{
		Enabled:     cfg.ClusterJoin.Enabled,
		ListenHost:  cfg.ClusterJoin.ListenHost,
		ListenPort:  cfg.ClusterJoin.ListenPort,
		AdminPort:   cfg.Server.AdminPort,
		TrustDomain: cfg.Auth.EffectiveClusterTrustDomain(),
		DataDir:     cfg.Raft.DataDir,
		NodeCertPEM: certPEM,
		Logger:      n.logger,
	})
	if err != nil {
		return &ClusterJoinError{Err: err}
	}
	n.clusterJoin = cj
	return nil
}

// ClusterJoinError reports that a clustered node's member-side CSR join
// could not be composed.
type ClusterJoinError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ClusterJoinError) Error() string {
	return "node: cluster join: " + e.Err.Error()
}

// Unwrap returns the cause.
func (e *ClusterJoinError) Unwrap() error { return e.Err }

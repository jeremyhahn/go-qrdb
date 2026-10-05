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
	"crypto/x509"

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
		TrustDomain: cfg.Auth.ClusterTrustDomain,
		Logger:      n.logger,
	}
	if c.TrustDomain == "" {
		c.TrustDomain = qrdbconfig.DefaultClusterTrustDomain
	}
	if id := n.clusterIdentity; id != nil {
		c.CertPEM, c.KeyPEM = id.Cert, id.Key
		if pool := x509.NewCertPool(); pool.AppendCertsFromPEM(id.CACert) {
			c.CACertPool = pool
		}
	}
	if authMW != nil {
		c.Revocation = authMW.RevocationChecker()
	}
	return lifecycle.ComposeRaftTenantCluster(c).ServerOptions()
}

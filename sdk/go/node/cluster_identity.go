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

	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
)

// defaultSingleNodeRaftAddress is the Raft address a single-member cluster
// configured with none runs under. Its host has no transport, so the address
// carries no traffic; it names the node, in its host config and in the SPIFFE
// URI of the certificate resolveClusterIdentity issues it.
const defaultSingleNodeRaftAddress = "127.0.0.1:63901"

// resolveClusterIdentity gives a single-member clustered node handed no
// cluster identity the one qrdbd gives itself with no TLS flags:
// clusterenroll.Resolve, the issuance path qrdbd's resolveTLS takes, loads the
// node certificate and cluster CA cached in dataDir, or, on the first boot,
// generates them there. The node then carries a node identity, so the
// composition both binaries share (lifecycle.ComposeRaftTenantCluster) wires
// the platform-init signer and the barrier-init watcher from it, and the
// cluster CA it generated is offered as Key A's bootstrap material, as qrdbd
// offers it.
//
// It resolves nothing for a node whose identity its embedder chose: one handed
// WithRaftMTLS, WithClusterCAKeyProvider or WithClusterCAMaterial. A
// multi-member node needs WithRaftMTLS for its transport, so it is never
// reached either. A node without an identity carries no signer, and refuses a
// Raft auto-unseal tenant create with *tenant.InitStatementRequiredError.
func (n *Node) resolveClusterIdentity(ctx context.Context, cfg *qrdbconfig.Config, members map[uint64]string, dataDir string) error {
	if n.clusterIdentity != nil || n.clusterCAKeyProvider != nil || len(n.clusterCACertPEM) != 0 || len(members) != 1 {
		return nil
	}
	raftAddress := cfg.Raft.RaftAddress
	if raftAddress == "" {
		raftAddress = defaultSingleNodeRaftAddress
	}
	mat, adv, err := clusterenroll.Resolve(ctx, &clusterenroll.Options{
		RaftAddress: raftAddress,
		ReplicaID:   cfg.Raft.ReplicaID,
		TrustDomain: cfg.Auth.EffectiveClusterTrustDomain(),
		DataDir:     dataDir,
		SingleNode:  true,
	}, n.logger)
	if err != nil {
		return &ClusterIdentityError{Err: err}
	}
	n.enrollAdvertiser = adv
	n.clusterIdentity = &RaftMTLSConfig{CACert: mat.CACertPEM, Cert: mat.CertPEM, Key: mat.KeyPEM}
	n.clusterCACertPEM = mat.CACertPEM
	n.clusterCAKeyPEM = mat.CAKeyPEM
	if len(mat.CAKeyPEM) != 0 {
		n.clusterCAKeyCacheDir = dataDir
	}
	return nil
}

// removeClusterCAKeyCache deletes the cluster CA key resolveClusterIdentity
// generated into the data directory once Key A is established in the
// replicated store, as qrdbd deletes its own, and drops the node's in-memory
// copy with it. A node whose key it did not generate has no cache to remove.
func (n *Node) removeClusterCAKeyCache() {
	if n.clusterCAKeyCacheDir == "" {
		return
	}
	lifecycle.RemoveClusterCAKeyCache(n.clusterCAKeyCacheDir, n.logger)
	n.clusterCAKeyCacheDir = ""
	n.clusterCAKeyPEM = nil
}

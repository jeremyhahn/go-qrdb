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
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// leafURIs is the URI SANs of the leaf certificate in certPEM.
func leafURIs(t *testing.T, certPEM []byte) []string {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	require.NotNil(t, block, "the node certificate is not PEM")
	leaf, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	uris := make([]string, 0, len(leaf.URIs))
	for _, u := range leaf.URIs {
		uris = append(uris, u.String())
	}
	return uris
}

// A single-member clustered node handed no identity resolves its own as qrdbd
// does with no TLS flags: a node certificate under the cluster trust domain,
// generated into its data directory on the first boot and loaded from there
// on the next, with the cluster CA key removed from disk once Key A is in the
// replicated store.
func TestNewClusteredNode_ResolvesItsOwnClusterIdentity(t *testing.T) {
	ctx := context.Background()
	cfg := newRaftIsolationConfig(t)

	n, err := NewClusteredNode(ctx, cfg, raftMembers(), nil, WithNoSyncWrites())
	require.NoError(t, err)
	require.NotNil(t, n.clusterIdentity, "a node handed no identity must resolve its own")
	require.False(t, n.byoTLS(), "a self-issued identity is not operator-supplied TLS")
	require.Contains(t, leafURIs(t, n.clusterIdentity.Cert),
		spiffe.NodeURI(qrdbconfig.DefaultClusterTrustDomain, cfg.Raft.RaftAddress))
	firstCert := n.clusterIdentity.Cert

	_, err = os.Stat(filepath.Join(cfg.Raft.DataDir, "node-cert.pem"))
	require.NoError(t, err, "the node certificate must be cached in the data directory")
	_, err = os.Stat(filepath.Join(cfg.Raft.DataDir, "ca-key.pem"))
	require.True(t, os.IsNotExist(err), "the cluster CA key must leave the disk once Key A is established: %v", err)
	require.Empty(t, n.clusterCAKeyPEM, "the node must drop its copy of the cluster CA key with the cache")
	require.NoError(t, n.Close())

	again, err := NewClusteredNode(ctx, cfg, raftMembers(), nil, WithNoSyncWrites())
	require.NoError(t, err)
	t.Cleanup(func() { _ = again.Close() })
	require.NotNil(t, again.clusterIdentity)
	require.Equal(t, firstCert, again.clusterIdentity.Cert, "a restart must load the cached identity, not issue another")
}

// The default Raft address of a single-member cluster names the node in its
// certificate.
func TestNewClusteredNode_ResolvedIdentityNamesTheDefaultRaftAddress(t *testing.T) {
	cfg := newRaftIsolationConfig(t)
	cfg.Raft.RaftAddress = ""

	n, err := NewClusteredNode(context.Background(), cfg, raftMembers(), nil, WithNoSyncWrites())
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	require.NotNil(t, n.clusterIdentity)
	require.Contains(t, leafURIs(t, n.clusterIdentity.Cert),
		spiffe.NodeURI(qrdbconfig.DefaultClusterTrustDomain, defaultSingleNodeRaftAddress))
}

// An identity that can be neither loaded nor generated fails construction
// with a typed error, rather than booting a node with no identity.
func TestNewClusteredNode_UnresolvableIdentityIsTyped(t *testing.T) {
	cfg := newRaftIsolationConfig(t)
	notADir := filepath.Join(t.TempDir(), "data")
	require.NoError(t, os.WriteFile(notADir, []byte("a file"), 0o600))
	cfg.Raft.DataDir = notADir

	n, err := NewClusteredNode(context.Background(), cfg, raftMembers(), nil, WithNoSyncWrites())
	var idErr *ClusterIdentityError
	require.ErrorAs(t, err, &idErr)
	require.Nil(t, n)
	require.NotNil(t, errors.Unwrap(idErr), "the error must carry its cause")
	require.Contains(t, idErr.Error(), "node: failed to resolve the cluster identity: ")
}

// An identity the embedder chose is never replaced, and a multi-member node,
// whose transport needs WithRaftMTLS, resolves nothing.
func TestResolveClusterIdentity_LeavesAnEmbeddersChoiceAlone(t *testing.T) {
	ctx := context.Background()
	cfg := newRaftIsolationConfig(t)
	given := &RaftMTLSConfig{Cert: []byte("given")}

	for name, tc := range map[string]struct {
		n       *Node
		members map[uint64]string
	}{
		"raft mTLS":        {n: &Node{clusterIdentity: given}, members: raftMembers()},
		"CA key provider":  {n: &Node{clusterCAKeyProvider: nopCAProvider{}}, members: raftMembers()},
		"CA material":      {n: &Node{clusterCACertPEM: []byte("ca")}, members: raftMembers()},
		"multiple members": {n: &Node{}, members: map[uint64]string{1: "127.0.0.1:1", 2: "127.0.0.1:2"}},
	} {
		t.Run(name, func(t *testing.T) {
			dataDir := t.TempDir()
			require.NoError(t, tc.n.resolveClusterIdentity(ctx, cfg, tc.members, dataDir))
			if tc.n.clusterIdentity != nil && tc.n.clusterIdentity != given {
				t.Fatal("the embedder's choice was replaced by a resolved identity")
			}
			entries, err := os.ReadDir(dataDir)
			require.NoError(t, err)
			require.Empty(t, entries, "nothing may be issued into the data directory")
		})
	}
}

// A node built without a node identity carries no platform-init signer, so
// the signed init statement every Raft auto-unseal tenant create requires
// cannot be made, and the create is refused with the typed error rather than
// let through unsigned.
func TestNode_ClusteredIsolationRaft_WithoutAnIdentityRefusesTheCreate(t *testing.T) {
	ctx := context.Background()
	cfg := newRaftIsolationConfig(t)
	ca, err := clustertls.GenerateCABundle()
	require.NoError(t, err)

	// Cluster CA material and no WithRaftMTLS: Key A is the embedder's, and
	// so is the choice of identity, which here is none.
	n, err := NewClusteredNode(ctx, cfg, raftMembers(), nil,
		WithTenantIsolation(),
		WithDefaultTenantEntitlements(raftEntitlements()),
		WithNoSyncWrites(),
		WithClusterCAMaterial(ca.CACertPEM, ca.CAKeyPEM),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	require.Nil(t, n.clusterIdentity)

	createDefaultPool(t, n)
	_, err = n.Services().Tenant.CreateTenant(ctx, "acme", "Acme Corp",
		tenant.IsolationRaft, tenant.WithBarrierAutoUnseal(true))
	var required *tenant.InitStatementRequiredError
	require.ErrorAs(t, err, &required, "an identity-less node must refuse the create")
}

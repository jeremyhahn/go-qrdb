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
	"context"
	"crypto/x509"
	"encoding/pem"
	"log/slog"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
	"github.com/jeremyhahn/go-qrdb/pkg/joinadvertise"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ClusterJoinConfig is what a node's member-side CSR join is composed from:
// the cluster_join configuration, and the node's own identity.
type ClusterJoinConfig struct {
	// Enabled starts the join listener and the join-token verb
	// (cluster_join.enabled).
	Enabled bool
	// ListenHost and ListenPort are the join listener's bind address.
	ListenHost string
	ListenPort int
	// AdminPort is this node's admin (REST) port, which a joiner is told.
	AdminPort int
	// TrustDomain is the cluster trust domain join identities are minted in.
	TrustDomain string
	// DataDir is the node's data directory (the listener's Noise key).
	DataDir string
	// NodeCertPEM is this node's own certificate, whose node identity is
	// recorded as admitted.
	NodeCertPEM []byte
	// Logger receives the listener's logs and the BYO-TLS warning.
	Logger *slog.Logger
}

// ClusterJoin is a node's member-side CSR join: the listener, and the store
// and provider the SPKI/Direct HTTPS twin route serves with.
type ClusterJoin struct {
	server      *clusterenroll.JoinServer
	store       kvstore.AtomicKVStore
	node        *Node
	trustDomain string
}

// Running reports whether the join listener was started.
func (j *ClusterJoin) Running() bool { return j != nil && j.server != nil }

// Addr is the join listener's address, empty when it is not running.
func (j *ClusterJoin) Addr() string {
	if !j.Running() {
		return ""
	}
	return j.server.Addr()
}

// PublicKey is the join listener's Noise static public key -- what a joiner
// pins -- or nil when the listener is not running.
func (j *ClusterJoin) PublicKey() []byte {
	if !j.Running() {
		return nil
	}
	return j.server.PublicKey()
}

// ServerOptions returns the server option registering the HTTPS join route
// beside the listener -- the same provider, store and trust domain -- or none
// when the listener is not running.
func (j *ClusterJoin) ServerOptions() []server.ServerOption {
	if !j.Running() {
		return nil
	}
	return []server.ServerOption{server.WithClusterJoin(j.node.ClusterCAProv, j.store, j.trustDomain)}
}

// Stop stops the join listener. It is safe on a nil or never-started join.
func (j *ClusterJoin) Stop() {
	if j == nil {
		return
	}
	j.server.Stop()
}

// StartClusterJoin composes member-side CSR join on a node, as qrdbd and a
// clustered sdk/go/node node both do. With a cluster CA key (n.ClusterCAProv)
// it records the node's own identity as admitted -- on every boot, enabled or
// not, so a later enable finds every member, founders included, refusing a
// token minted for its node ID -- and, when enabled, starts the join listener
// and binds the admin join-token verb to the SAME store the listener
// consumes tokens from. Without a cluster CA key (BYO-TLS) there is nothing
// to sign a joiner's CSR with: an enabled join is not started, with a
// warning. The caller must already know the system shard's leader.
func StartClusterJoin(ctx context.Context, n *Node, c ClusterJoinConfig) (*ClusterJoin, error) {
	logger := c.Logger
	if logger == nil {
		logger = slog.Default()
	}
	j := &ClusterJoin{node: n, trustDomain: c.TrustDomain}
	if n.ClusterCAProv == nil {
		if c.Enabled {
			logger.Warn("member-side CSR join requested but this node has no cluster CA key (BYO-TLS); " +
				"the join listener was not started. Inject a cakey.Provider or use a cluster-managed CA to enable it.")
		}
		return j, nil
	}
	store := newKVStoreAdapter(n.KV, nil)
	if err := recordOwnJoinIdentity(ctx, store, c.NodeCertPEM, c.TrustDomain); err != nil {
		return nil, err
	}
	if !c.Enabled {
		return j, nil
	}
	var advertiser clusterenroll.FeatureAdvertiser
	if a := joinadvertise.New(n.Lease, n.MVCC); a != nil {
		advertiser = a
	}
	srv, err := clusterenroll.StartJoinServer(n.ClusterCAProv, clusterenroll.JoinServerOptions{
		Host:        c.ListenHost,
		Port:        c.ListenPort,
		Store:       store,
		TrustDomain: c.TrustDomain,
		AdminPort:   c.AdminPort,
		Advertiser:  advertiser,
	}, c.DataDir, logger)
	if err != nil {
		return nil, err
	}
	j.server, j.store = srv, store
	// The admin:cluster:join_token verb mints into the store the listener
	// consumes from: one join-token store per node.
	if adminSvc, ok := n.Admin.(*service.AdminService); ok {
		adminSvc.SetClusterJoinTokenStore(store, c.TrustDomain)
	}
	return j, nil
}

// JoinIdentityRecordError reports that this node's own join identity could
// not be recorded as admitted.
type JoinIdentityRecordError struct {
	Err error
}

// Error returns a human-readable description.
func (e *JoinIdentityRecordError) Error() string {
	return "lifecycle: recording this node's join identity failed: " + e.Err.Error()
}

// Unwrap returns the cause.
func (e *JoinIdentityRecordError) Unwrap() error { return e.Err }

// recordOwnJoinIdentity marks this node's own SPIFFE node identity admitted
// in the replicated join-identity ledger (clusterenroll's
// __sys:cluster_join_identities/ records), the same ledger MintJoinToken
// consults to refuse minting for an identity already in the cluster. It is
// what makes "existing member" mean "the cluster CA has issued this node
// identity" for every founder and first-boot node, not only for nodes that
// arrived through the token path (which already records on successful
// issuance; see clusterenroll.HandleJoinRequest).
//
// Called on every boot that reaches a durable ClusterCAProv -- idempotent
// (PutIfAbsent, like every other call site of
// clusterenroll.MarkJoinIdentityAdmitted), so a restart after the first
// successful call is a cheap no-op, not a second write.
//
// certPEM is this node's own leaf, whose URI SAN is read back rather than
// recomputed from configuration: the committed identity is whatever was
// baked into the certificate at generation (Resolve's single-node/first-
// node paths, or Join's token path), which must not silently drift from a
// later change to --raft-address or --cluster-trust-domain. The leaf's own
// NotAfter and serial are read back the same way and threaded into
// MarkJoinIdentityAdmitted, so this node's own identity can later be
// detected as LAPSED (and renewed) exactly like one admitted through the
// token path.
//
// A leaf with no node SPIFFE URI SAN at all -- an operator-supplied
// certificate from explicit TLS files (--tls-ca/--tls-cert/--tls-key),
// signed outside this package's control -- has no identity to record here.
// That is a known, documented gap: such a node's own identity is never
// entered into the ledger, so MintJoinToken cannot refuse re-minting a
// token for whatever node ID an admin later chooses to name it. Closing
// that gap requires recording the identity at WHATEVER point such a node
// is admitted to the cluster (e.g. the admin's AddMember call), which no
// current admission path threads a SPIFFE node URI through -- AddMember
// takes an spki:<hex> Raft identity, a different namespace entirely (see
// pkg/cluster/member_identity.go). Closing it needs a decision on which
// admission call gains a node-URI parameter, which is why it is documented
// here rather than guessed at.
func recordOwnJoinIdentity(ctx context.Context, store kvstore.AtomicKVStore, certPEM []byte, trustDomain string) error {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil
	}
	identity, err := spiffe.NodeFromCert(cert, trustDomain)
	if err != nil {
		// No node SPIFFE URI SAN (or one for a foreign trust domain): the
		// documented BYO-TLS gap above. Not an error -- this node's
		// certificate was never going to carry a cluster-join identity.
		return nil
	}
	if err := clusterenroll.MarkJoinIdentityAdmitted(ctx, store, identity.String(), cert.NotAfter, cert.SerialNumber.String()); err != nil {
		return &JoinIdentityRecordError{Err: err}
	}
	return nil
}

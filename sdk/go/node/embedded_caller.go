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
	"crypto"
	"crypto/tls"
	"crypto/x509"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// embeddedCallerField names WithEmbeddedCaller in a *ConfigError.
const embeddedCallerField = "WithEmbeddedCaller"

// WithEmbeddedCaller configures the identity the node's embedded client
// forwards as (docs/replica-forwarding). A clustered node that holds no
// replica of a database cannot serve a call to it; with a caller configured,
// the embedded client forwards the call over QUIC to a replica, carrying this
// identity in the signed on-behalf assertion, and the replica authorizes that
// identity -- never the node's. Without one, such a call is refused with the
// typed *placement.NotAReplicaError.
//
// cert is a client certificate and its private key, as the caller would
// present to a QUIC listener. Each forward authenticates it the way the QUIC
// listener authenticates a client: the chain must verify, for client
// authentication, to the node's CA pool (server.WithCACertPool), and the
// identity is what the node's one authentication chain derives from it -- the
// mTLS authenticator, or an authenticator a composer added with
// WithAuthenticators, such as an attested-host authenticator. A revoked or
// expired certificate therefore stops forwarding at once.
//
// The node refuses to start with a certificate that is empty, does not parse
// or does not match its private key (*ConfigError).
func WithEmbeddedCaller(cert tls.Certificate) NodeOption {
	return func(o *nodeOptions) {
		c := cert
		o.embeddedCaller = &c
	}
}

// parseEmbeddedCaller returns the certificate chain of the configured caller,
// leaf first, or nil when none was configured.
func parseEmbeddedCaller(cert *tls.Certificate) ([]*x509.Certificate, error) {
	if cert == nil {
		return nil, nil
	}
	if len(cert.Certificate) == 0 {
		return nil, &ConfigError{Field: embeddedCallerField, Message: "carries no certificate"}
	}
	chain := make([]*x509.Certificate, 0, len(cert.Certificate))
	for _, der := range cert.Certificate {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, &ConfigError{Field: embeddedCallerField, Message: "certificate does not parse", Err: err}
		}
		chain = append(chain, c)
	}
	signer, ok := cert.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, &ConfigError{Field: embeddedCallerField, Message: "carries no private key"}
	}
	pub, ok := signer.Public().(interface{ Equal(crypto.PublicKey) bool })
	if !ok || !pub.Equal(chain[0].PublicKey) {
		return nil, &ConfigError{Field: embeddedCallerField, Message: "private key does not match the certificate"}
	}
	return chain, nil
}

// embeddedCaller authenticates the configured caller's certificate through
// the node's current server, on every call.
type embeddedCaller struct {
	node  *Node
	chain []*x509.Certificate
}

var _ auth.Authenticator = (*embeddedCaller)(nil)

// Authenticate returns the identity the node's authentication chain derives
// from the configured certificate.
func (c *embeddedCaller) Authenticate(ctx context.Context) (*auth.Identity, error) {
	srv := c.node.srv.Load()
	if srv == nil {
		return nil, &service.DataPlaneRecyclingError{Service: "embedded caller"}
	}
	return srv.AuthenticateCertificate(ctx, c.chain)
}

// Name returns the authenticator type.
func (c *embeddedCaller) Name() string { return "embedded-caller" }

// nodeReplicaForwarder forwards through the node's current server's frame
// forwarder.
type nodeReplicaForwarder struct {
	node *Node
}

var _ embedded.ReplicaForwarder = (*nodeReplicaForwarder)(nil)

// Forward forwards the frame through the current server's forwarder; it
// forwards nothing while the node has no server or forwarding is off.
func (f *nodeReplicaForwarder) Forward(ctx context.Context, caller *auth.Identity, method, database string, payload []byte, refusal *placement.NotAReplicaError) (uint16, []byte, bool) {
	srv := f.node.srv.Load()
	if srv == nil {
		return 0, nil, false
	}
	fw, err := srv.ReplicaForwarder()
	if err != nil {
		f.node.logger.Warn("replicaforward: embedded call not forwarded", "tenant", refusal.TenantID, "database", refusal.Database, "error", err)
		return 0, nil, false
	}
	if fw == nil {
		return 0, nil, false
	}
	return fw.Forward(ctx, caller, method, database, payload, refusal)
}

// ForwardToLeader delegates to the node's *quic.FrameForwarder exactly as
// Forward does, for a leader-only refusal rather than a non-replica one.
func (f *nodeReplicaForwarder) ForwardToLeader(ctx context.Context, caller *auth.Identity, method, database string, payload []byte, leaderAddr string) (uint16, []byte, bool) {
	srv := f.node.srv.Load()
	if srv == nil {
		return 0, nil, false
	}
	fw, err := srv.ReplicaForwarder()
	if err != nil {
		f.node.logger.Warn("admin forwarding: embedded call not forwarded", "leader_raft_addr", leaderAddr, "error", err)
		return 0, nil, false
	}
	if fw == nil {
		return 0, nil, false
	}
	return fw.ForwardToLeader(ctx, caller, method, database, payload, leaderAddr)
}

// embeddedForwardingOption returns the option that makes the node's embedded
// client forward as the configured caller, or nil -- every such call is then
// refused -- on a standalone node, with forwarding turned off, or with no
// caller configured.
func (n *Node) embeddedForwardingOption(standalone, forwardingOn bool) embedded.Option {
	if standalone || !forwardingOn || len(n.embeddedCaller) == 0 {
		return nil
	}
	return embedded.WithReplicaForwarding(&nodeReplicaForwarder{node: n}, &embeddedCaller{node: n, chain: n.embeddedCaller})
}

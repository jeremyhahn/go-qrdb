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

package middleware

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// issuerCA is a test CA: the cluster's, or a tenant's Key B CA.
type issuerCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newIssuerCA(t *testing.T, cn string) issuerCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return issuerCA{cert: cert, key: key}
}

// nodeLeaf issues a leaf carrying the node SPIFFE SAN for nodeID: a cluster
// node's when ca is the cluster CA, a forged one when it is a Key B CA.
func (ca issuerCA) nodeLeaf(t *testing.T, nodeID string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "qrdb-node"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		URIs: []*url.URL{{Scheme: "spiffe", Host: "qrdb.cluster", Path: "/node/" + nodeID}}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

// clusterOnly is an issuer verifier whose cluster CA is cluster: on a node
// with operator-supplied TLS, the operator's peer CA.
type clusterOnly struct{ cluster *x509.Certificate }

func (v clusterOnly) VerifyPeerChain(context.Context, *x509.Certificate, []*x509.Certificate) ([]*x509.Certificate, error) {
	return nil, auth.ErrInvalidCertificate
}

func (v clusterOnly) IsClusterCA(_ context.Context, issuer *x509.Certificate) bool {
	return issuer != nil && issuer.Equal(v.cluster)
}

func (v clusterOnly) VerifyIssuer(ctx context.Context, _ string, chain []*x509.Certificate) error {
	if len(chain) < 2 {
		return auth.ErrInvalidCertificate
	}
	if v.IsClusterCA(ctx, chain[1]) {
		return nil
	}
	return auth.ErrInvalidCertificate
}

func issuerOBMiddleware(v auth.IssuerVerifier) *AuthMiddleware {
	stored := &user.User{Username: "alice", TenantID: "acme", Lifecycle: user.LifecycleActive, RoleIDs: []string{"kv-writer"}}
	return New(&capturingAuthorizer{}, WithAuthenticator(&mockAuthenticator{name: "mtls", err: auth.ErrNoPeerCertificate}),
		WithUserService(&mockUserServicer{user: stored}), WithIssuerVerifier(v))
}

// chainState is the verified TLS state of a peer presenting leaf, issued by
// issuer.
func chainState(leaf, issuer *x509.Certificate) *tls.ConnectionState {
	return &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf, issuer}, VerifiedChains: [][]*x509.Certificate{{leaf, issuer}}}
}

// Every transport's on-behalf path admits a forward only from a node
// certificate the cluster CA issued -- on a node with operator-supplied TLS,
// the operator's peer CA. A tenant's Key B CA, trusted on the same listener,
// issuing a leaf with a forged node SAN, is refused on REST, gRPC (and so the
// Unix socket) and QUIC alike, before anything the request carries is read.
func TestOnBehalf_OnlyTheClusterCAsNodeMayForward(t *testing.T) {
	clusterCA := newIssuerCA(t, "operator-peer-ca")
	keyB := newIssuerCA(t, "tenant-key-b-ca")
	m := issuerOBMiddleware(clusterOnly{cluster: clusterCA.cert})
	payload := []byte("kv put payload")
	grpcReq := &pb.PutRequest{Key: "k", Value: []byte("v")}

	transports := map[string]func(leaf *x509.Certificate, key *ecdsa.PrivateKey, issuer *x509.Certificate) (*auth.Identity, error){
		"rest": func(leaf *x509.Certificate, key *ecdsa.PrivateKey, issuer *x509.Certificate) (*auth.Identity, error) {
			r := buildOnBehalfRequest(t, leaf, key, "alice", "acme", obNodeID, time.Now(), []byte("v"), "PUT", "/kv/k")
			r.TLS = chainState(leaf, issuer)
			_, id, err := m.authenticateOnBehalf(context.Background(), httptest.NewRecorder(), r)
			return id, err
		},
		"grpc": func(leaf *x509.Certificate, key *ecdsa.PrivateKey, issuer *x509.Certificate) (*auth.Identity, error) {
			msg, err := proto.MarshalOptions{Deterministic: true}.Marshal(grpcReq)
			if err != nil {
				t.Fatal(err)
			}
			ab, sb, err := forward.Sign(forward.RPCBinding("grpc", "/qrdb.v1.QRDBService/Put", msg).Assertion("alice", "acme", obNodeID, time.Now()), key)
			if err != nil {
				t.Fatal(err)
			}
			ctx := peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: *chainState(leaf, issuer)}})
			_, id, err := m.authenticateGRPCOnBehalf(ctx, "/qrdb.v1.QRDBService/Put", grpcReq, ab, sb)
			return id, err
		},
		"quic": func(leaf *x509.Certificate, key *ecdsa.PrivateKey, issuer *x509.Certificate) (*auth.Identity, error) {
			ab, sb, err := forward.Sign(forward.RPCBinding("quic", "kv.Put", payload).Assertion("alice", "acme", obNodeID, time.Now()), key)
			if err != nil {
				t.Fatal(err)
			}
			ctx, err := m.AuthenticateQUICOnBehalf(auth.WithTLSState(context.Background(), chainState(leaf, issuer)), "kv.Put", payload, ab, sb)
			return auth.IdentityFromContext(ctx), err
		},
	}
	for name, run := range transports {
		t.Run(name, func(t *testing.T) {
			leaf, key := clusterCA.nodeLeaf(t, obNodeID)
			if id, err := run(leaf, key, clusterCA.cert); err != nil || id == nil || id.Subject != "alice" {
				t.Fatalf("the cluster CA's node: %+v, %v; want the asserted caller", id, err)
			}
			forged, fkey := keyB.nodeLeaf(t, obNodeID)
			id, err := run(forged, fkey, keyB.cert)
			var authErr *auth.AuthError
			if err == nil || !errors.As(err, &authErr) || (id != nil && id.Subject == "alice") {
				t.Fatalf("a Key B leaf with a forged node SAN: %+v, %v; want the opaque refusal", id, err)
			}
		})
	}
}

// With no chain at all the gate refuses rather than indexing an empty slice.
func TestOnBehalfPeerGate_NoChain(t *testing.T) {
	m := issuerOBMiddleware(clusterOnly{})
	if err := m.onBehalfPeerGate(context.Background(), nil); !errors.Is(err, auth.ErrNoPeerCertificate) {
		t.Fatalf("onBehalfPeerGate(no chain) = %v; want ErrNoPeerCertificate", err)
	}
}

// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build !integration

package node

import (
	"context"
	"crypto/x509"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	quictransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/quic"
)

// chainCertSubject is the account the test client certificate names.
const chainCertSubject = "chain-client"

// chainRecorder is a consumer authenticator that declines every request and
// counts how often the chain consulted it: a count of zero means something
// ahead of it decided the request.
type chainRecorder struct{ calls atomic.Int64 }

var _ sdk.Authenticator = (*chainRecorder)(nil)

func (r *chainRecorder) Authenticate(context.Context) (*sdk.Identity, error) {
	r.calls.Add(1)
	return nil, sdk.ErrUnauthenticated
}

func (r *chainRecorder) Name() string { return "chain-recorder" }

// chainTransports is every network transport a client certificate reaches
// the chain on: the bearer transports plus native QUIC, whose frames carry
// no bearer but whose connection carries the certificate.
func chainTransports() []bearerTransport {
	return append(bearerTransports(), bearerTransport{
		name: "quic",
		configure: func(t *testing.T, cfg *qrdbconfig.Config) {
			cfg.Protocols.QUIC = true
			cfg.Server.QUICPort = freeUDPPort(t)
		},
		call: func(ctx context.Context, n *Node, ca *qrdbdharness.CA, cred callerCredential) error {
			tr, err := quictransport.New(transport.WithAddress(n.Server().QUICAddr()), transport.WithTLSConfig(cred.clientTLS(ca)))
			if err != nil {
				return err
			}
			defer func() { _ = tr.Close() }()
			if err := tr.Connect(ctx); err != nil {
				return err
			}
			_, err = tr.Status(ctx)
			return err
		},
		// Connect's health check is an authenticated call that admits a
		// permission-denied caller and fails closed only on an unauthenticated
		// one, so a caller no authenticator accepts is refused at Connect with
		// a *transport.ConnectionError; a later call reports the wire status.
		unauthenticated: func(err error) bool {
			var re *transport.RequestError
			if errors.As(err, &re) {
				return re.StatusCode == int(wire.StatusUnauthenticated)
			}
			var ce *transport.ConnectionError
			return errors.As(err, &ce)
		},
	})
}

// TestWithAuthenticators_PositionDecidesACertificateRequestOnEveryTransport
// sends a request that presents a client certificate enrolled on an account
// holding admin:status, on every network transport, with a declining consumer
// authenticator at each position:
//
//   - before mTLS, the consumer authenticator is consulted first and, having
//     declined, mTLS admits the request;
//   - after mTLS, mTLS decides and the consumer authenticator never runs;
//   - replacing mTLS, the consumer authenticator is consulted and nothing
//     else is, so the certificate alone is refused as unauthenticated -- the
//     transport's TLS still verified it, but no authenticator accepts it.
func TestWithAuthenticators_PositionDecidesACertificateRequestOnEveryTransport(t *testing.T) {
	cases := []struct {
		pos       sdk.ChainPosition
		admitted  bool
		consulted bool
	}{
		{sdk.ChainBeforeMTLS, true, true},
		{sdk.ChainAfterMTLS, true, false},
		{sdk.ChainReplaceMTLS, false, true},
	}
	for _, tc := range cases {
		for _, ct := range chainTransports() {
			t.Run(tc.pos.String()+"/"+ct.name, func(t *testing.T) {
				rec := &chainRecorder{}
				n, ca := newBearerNode(t, ct.configure, WithAuthenticators(tc.pos, rec))
				cred := enrolChainClient(t, n, ca)
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()

				err := ct.call(ctx, n, ca, cred)
				if tc.admitted {
					require.NoError(t, err, "an enrolled certificate was refused")
				} else {
					require.Error(t, err, "a certificate alone was admitted with mTLS replaced")
					require.True(t, ct.unauthenticated(err), "refusal is not unauthenticated: %v", err)
				}
				require.Equal(t, tc.consulted, rec.calls.Load() > 0, "consumer authenticator consulted %d times", rec.calls.Load())
			})
		}
	}
}

// TestWithAuthenticators_PositionDecidesTheEmbeddedCaller covers the embedded
// client's forwarding caller (WithEmbeddedCaller), which the node
// authenticates through the same chain as a QUIC client certificate.
func TestWithAuthenticators_PositionDecidesTheEmbeddedCaller(t *testing.T) {
	cases := []struct {
		pos       sdk.ChainPosition
		admitted  bool
		consulted bool
	}{
		{sdk.ChainBeforeMTLS, true, true},
		{sdk.ChainAfterMTLS, true, false},
		{sdk.ChainReplaceMTLS, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.pos.String(), func(t *testing.T) {
			rec := &chainRecorder{}
			n, ca := newBearerNode(t, func(*testing.T, *qrdbconfig.Config) {}, WithAuthenticators(tc.pos, rec))
			cred := enrolChainClient(t, n, ca)
			leaf, err := x509.ParseCertificate(cred.cert.Certificate[0])
			require.NoError(t, err)

			id, err := n.Server().AuthenticateCertificate(context.Background(), []*x509.Certificate{leaf})
			if tc.admitted {
				require.NoError(t, err)
				// The certificate resolved to the chain-client account, so the
				// Subject is that account's user id, not the name it carries.
				require.Equal(t, chainCertSubject, id.Username)
				require.NotZero(t, id.UserID)
				require.Equal(t, auth.UserSubject(id.UserID), id.Subject)
			} else {
				require.ErrorIs(t, err, sdk.ErrUnauthenticated)
			}
			require.Equal(t, tc.consulted, rec.calls.Load() > 0, "consumer authenticator consulted %d times", rec.calls.Load())
		})
	}
}

// TestWithAuthenticators_RefusesAnInvalidChain: a position that is none of
// the three, and replacing mTLS with nothing, each fail node construction
// with their typed error rather than booting a chain nobody asked for.
func TestWithAuthenticators_RefusesAnInvalidChain(t *testing.T) {
	cases := []struct {
		name   string
		opt    NodeOption
		target any
	}{
		{"zero position", WithAuthenticators(0, &chainRecorder{}), new(*sdk.InvalidChainPositionError)},
		{"unknown position", WithAuthenticators(sdk.ChainReplaceMTLS+1, &chainRecorder{}), new(*sdk.InvalidChainPositionError)},
		{"position with no authenticator", WithAuthenticators(0), new(*sdk.InvalidChainPositionError)},
		{"replaced with nothing", WithAuthenticators(sdk.ChainReplaceMTLS), new(*sdk.EmptyAuthenticatorChainError)},
		{"replaced with nil", WithAuthenticators(sdk.ChainReplaceMTLS, nil), new(*sdk.EmptyAuthenticatorChainError)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewStandaloneNode(context.Background(), newMemoryNodeCfg(t), nil, tc.opt)
			require.Error(t, err)
			var wiring *AuthMiddlewareWiringError
			require.ErrorAs(t, err, &wiring)
			require.ErrorAs(t, err, tc.target)
		})
	}
}

// enrolChainClient issues a client certificate naming chainCertSubject in the
// system tenant, and creates that account with admin:status and the
// certificate enrolled, so mTLS admits it under the default binding policy.
func enrolChainClient(t *testing.T, n *Node, ca *qrdbdharness.CA) callerCredential {
	t.Helper()
	kp, err := ca.Issue(t.TempDir(), qrdbdharness.WithSubject(chainCertSubject, tenantid.System))
	require.NoError(t, err)
	cert, err := kp.TLSCertificate()
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	require.NoError(t, err)

	ctx := context.Background()
	statusRole, err := n.Services().Role.CreateRole(ctx, &role.CreateRoleRequest{
		Name:        "chain-status",
		TenantID:    tenantid.System,
		Permissions: []permission.Permission{permission.AdminStatus},
	})
	require.NoError(t, err)
	u, err := n.Services().User.CreateUser(ctx, &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             chainCertSubject,
		TenantID:             tenantid.System,
		RoleIDs:              []string{statusRole.ID},
	})
	require.NoError(t, err)
	_, err = n.Services().User.AddCertBinding(ctx, u.ID, user.ComputeSPKIThumbprint(leaf), 0)
	require.NoError(t, err)
	return callerCredential{cert: &cert}
}

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
	"crypto/tls"
	"crypto/x509"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// quicOnBehalf signs an assertion for (method, payload) with signKey and
// returns a context carrying peerCert's verified TLS state, as the QUIC
// server builds it from the connection.
func quicOnBehalf(t *testing.T, peerCert *x509.Certificate, signKey *ecdsa.PrivateKey, method string, payload []byte) (context.Context, string, string) {
	t.Helper()
	a := forward.RPCBinding("quic", method, payload).Assertion(obAliceSubject, "acme", obNodeID, time.Now())
	ab, sb, err := forward.Sign(a, signKey)
	if err != nil {
		t.Fatal(err)
	}
	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{peerCert}, VerifiedChains: [][]*x509.Certificate{{peerCert}}}
	return auth.WithTLSState(context.Background(), state), ab, sb
}

func quicOBMiddleware() *AuthMiddleware {
	stored := &user.User{ID: obAliceID, Username: "alice", TenantID: "acme", Lifecycle: user.LifecycleActive, RoleIDs: []string{"kv-writer"}}
	return newTestMiddleware(&capturingAuthorizer{}, WithAuthenticator(&mockAuthenticator{name: "mtls", err: auth.ErrNoPeerCertificate}),
		WithUserService(&mockUserServicer{user: stored}))
}

func TestQUICOnBehalf_ValidAuthenticatesAsTheCaller(t *testing.T) {
	cert, key := obNodeCert(t, obNodeID)
	payload := []byte("kv put payload")
	ctx, ab, sb := quicOnBehalf(t, cert, key, "kv.Put", payload)
	got, err := quicOBMiddleware().AuthenticateQUICOnBehalf(ctx, "kv.Put", payload, ab, sb)
	if err != nil {
		t.Fatalf("AuthenticateQUICOnBehalf = %v", err)
	}
	if id := auth.IdentityFromContext(got); id == nil || id.Subject != obAliceSubject || id.TenantID != "acme" || !isOnBehalfIdentity(id) {
		t.Fatalf("identity = %+v; want the asserted caller, marked on-behalf", id)
	}
}

func TestQUICOnBehalf_RefusesEveryTamperAndANonClusterPeer(t *testing.T) {
	cert, key := obNodeCert(t, obNodeID)
	userCert, userKey := obUserCert(t)
	payload := []byte("kv put payload")
	m := quicOBMiddleware()
	for name, run := range map[string]func() error{
		"payload swapped": func() error {
			ctx, ab, sb := quicOnBehalf(t, cert, key, "kv.Put", payload)
			_, err := m.AuthenticateQUICOnBehalf(ctx, "kv.Put", []byte("other"), ab, sb)
			return err
		},
		"method swapped": func() error {
			ctx, ab, sb := quicOnBehalf(t, cert, key, "kv.Put", payload)
			_, err := m.AuthenticateQUICOnBehalf(ctx, "kv.Delete", payload, ab, sb)
			return err
		},
		"bound to another transport": func() error {
			a := forward.RPCBinding("grpc", "kv.Put", payload).Assertion(obAliceSubject, "acme", obNodeID, time.Now())
			ab, sb, _ := forward.Sign(a, key)
			ctx, _, _ := quicOnBehalf(t, cert, key, "kv.Put", payload)
			_, err := m.AuthenticateQUICOnBehalf(ctx, "kv.Put", payload, ab, sb)
			return err
		},
		"non-cluster peer": func() error {
			ctx, ab, sb := quicOnBehalf(t, userCert, userKey, "kv.Put", payload)
			_, err := m.AuthenticateQUICOnBehalf(ctx, "kv.Put", payload, ab, sb)
			return err
		},
		"no TLS state": func() error {
			_, ab, sb := quicOnBehalf(t, cert, key, "kv.Put", payload)
			_, err := m.AuthenticateQUICOnBehalf(context.Background(), "kv.Put", payload, ab, sb)
			return err
		},
	} {
		if err := run(); err == nil {
			t.Errorf("%s: authenticated; want refused", name)
		}
	}
}

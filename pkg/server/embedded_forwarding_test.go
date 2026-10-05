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

package server

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward/replicaforwardtest"
)

// callerChain returns the certificate chain of a caller certificate ca
// issued.
func callerChain(t *testing.T, ca *replicaforwardtest.CA) []*x509.Certificate {
	t.Helper()
	c, err := x509.ParseCertificate(ca.TLS(t, "").Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return []*x509.Certificate{c}
}

// attestedHost is a composer's authenticator: it admits the peer whose
// verified certificate's thumbprint it holds. It stands for an attested-host
// authenticator, which reads the connection's certificate the same way.
type attestedHost struct{ thumbprint string }

func (a attestedHost) Authenticate(ctx context.Context) (*auth.Identity, error) {
	st := auth.TLSStateFromContext(ctx)
	if st == nil || len(st.VerifiedChains) == 0 {
		return nil, auth.ErrNoPeerCertificate
	}
	sum := sha256.Sum256(st.VerifiedChains[0][0].Raw)
	if hex.EncodeToString(sum[:]) != a.thumbprint {
		return nil, auth.ErrUnauthenticated
	}
	return &auth.Identity{Subject: "host-7", TenantID: "acme", Metadata: map[string]string{}}, nil
}

func (attestedHost) Name() string { return "attested-host" }

func serverWith(opts ...ServerOption) *Server {
	s := &Server{config: &config.Config{}, logger: slog.New(slog.DiscardHandler)}
	for _, o := range opts {
		o(&s.opts)
	}
	return s
}

func TestAuthenticateCertificate(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	other := replicaforwardtest.NewCA(t)
	chain := callerChain(t, ca)
	sum := sha256.Sum256(chain[0].Raw)
	// This file is about caller authentication for replica forwarding, not
	// auth.mtls_binding_policy (go-qrdb#517/#548): mtls has no user store
	// wired at all, and under the default MTLSBindingRequired that refuses
	// every certificate login as naming no account. MTLSBindingEnrolled is
	// the documented explicit opt-out for a policy these subtests were
	// never meant to exercise.
	mtls := authmw.New(&auth.NoopAuthorizer{}, authmw.WithAuthenticator(auth.NewMTLSAuthenticator()),
		authmw.WithMTLSBindingPolicy(auth.MTLSBindingEnrolled))
	attested := authmw.New(&auth.NoopAuthorizer{}, authmw.WithAuthenticator(attestedHost{thumbprint: hex.EncodeToString(sum[:])}))

	for name, tc := range map[string]struct {
		srv     *Server
		chain   []*x509.Certificate
		subject string
		check   func(error) bool
	}{
		"no authentication chain": {srv: serverWith(WithCACertPool(ca.Pool)), chain: chain,
			check: func(err error) bool { var e *CallerAuthUnavailableError; return errors.As(err, &e) }},
		"no certificate": {srv: serverWith(WithCACertPool(ca.Pool), WithAuthMiddleware(mtls)),
			check: func(err error) bool { var e *CallerCertificateError; return errors.As(err, &e) && e.Unwrap() == nil }},
		"no CA pool": {srv: serverWith(WithAuthMiddleware(mtls)), chain: chain,
			check: func(err error) bool { var e *CallerCertificateError; return errors.As(err, &e) }},
		"another CA's certificate": {srv: serverWith(WithCACertPool(other.Pool), WithAuthMiddleware(mtls)), chain: chain,
			check: func(err error) bool { var e *CallerCertificateError; return errors.As(err, &e) && e.Unwrap() != nil }},
		"the mTLS authenticator":     {srv: serverWith(WithCACertPool(ca.Pool), WithAuthMiddleware(mtls)), chain: chain, subject: "forwardtest-peer"},
		"a composer's authenticator": {srv: serverWith(WithCACertPool(ca.Pool), WithAuthMiddleware(attested)), chain: chain, subject: "host-7"},
		"a composer's authenticator refusing": {srv: serverWith(WithCACertPool(ca.Pool), WithAuthMiddleware(attested)), chain: callerChain(t, ca),
			check: func(err error) bool { return errors.Is(err, auth.ErrUnauthenticated) }},
	} {
		t.Run(name, func(t *testing.T) {
			id, err := tc.srv.AuthenticateCertificate(context.Background(), tc.chain)
			if tc.check != nil {
				if id != nil || !tc.check(err) {
					t.Fatalf("AuthenticateCertificate = %+v, %v; want the typed refusal", id, err)
				}
				if err.Error() == "" {
					t.Fatal("refusal has no message")
				}
				return
			}
			if err != nil || id == nil || id.Subject != tc.subject {
				t.Fatalf("AuthenticateCertificate = %+v, %v; want subject %q", id, err, tc.subject)
			}
		})
	}
}

func TestReplicaForwarder_OffWithoutForwarding(t *testing.T) {
	f, err := serverWith().ReplicaForwarder()
	if f != nil || err != nil {
		t.Fatalf("ReplicaForwarder = %v, %v; want none on a server that does not forward", f, err)
	}
}

func TestReplicaForwarder_OneForwarderUntilTheServerStops(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	locator := replicaforward.NewLocator(nil, replicaforward.NewLeaderCache(), "a:1", nil)
	s := serverWith(WithTLS(ca.TLS(t, "node-a")), WithCACertPool(ca.Pool), WithReplicaForwarding(locator, time.Second))

	first, err := s.ReplicaForwarder()
	if err != nil || first == nil {
		t.Fatalf("ReplicaForwarder = %v, %v; want a forwarder", first, err)
	}
	if again, _ := s.ReplicaForwarder(); again != first {
		t.Fatal("a second call built a second forwarder")
	}
	s.stopReplicaForwarder()
	var stopped *EmbeddedForwardingStoppedError
	if f, err := s.ReplicaForwarder(); f != nil || !errors.As(err, &stopped) || err.Error() == "" {
		t.Fatalf("ReplicaForwarder after stop = %v, %v; want *EmbeddedForwardingStoppedError", f, err)
	}
}

func TestReplicaForwarder_NoTLSIsTheTLSError(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	locator := replicaforward.NewLocator(nil, replicaforward.NewLeaderCache(), "a:1", nil)
	s := serverWith(WithCACertPool(ca.Pool), WithReplicaForwarding(locator, time.Second))
	var tlsErr *TLSUnavailableError
	if f, err := s.ReplicaForwarder(); f != nil || !errors.As(err, &tlsErr) {
		t.Fatalf("ReplicaForwarder = %v, %v; want *TLSUnavailableError", f, err)
	}
}

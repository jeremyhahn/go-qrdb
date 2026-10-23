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

package service

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
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
)

// clientCertVerifier answers every chain with err, or with the leaf alone.
type clientCertVerifier struct {
	err   error
	calls int
}

var _ auth.IssuerVerifier = (*clientCertVerifier)(nil)

func (v *clientCertVerifier) VerifyPeerChain(_ context.Context, leaf *x509.Certificate, _ []*x509.Certificate) ([]*x509.Certificate, error) {
	v.calls++
	if v.err != nil {
		return nil, v.err
	}
	return []*x509.Certificate{leaf}, nil
}

func (v *clientCertVerifier) IsClusterCA(context.Context, *x509.Certificate) bool { return false }

func (v *clientCertVerifier) VerifyIssuer(context.Context, string, []*x509.Certificate) error {
	return nil
}

// unreadTrustError is a verifier refusal that could not read trust state.
type unreadTrustError struct{}

func (unreadTrustError) Error() string               { return "trust state unread" }
func (unreadTrustError) TrustStateUnavailable() bool { return true }

func selfSignedClientCert(t *testing.T) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "holder"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestVerifiedClientCertThumbprint_RefusesUnverified(t *testing.T) {
	cert := selfSignedClientCert(t)
	presented := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	for name, ctx := range map[string]context.Context{
		"no tls state":     context.Background(),
		"no certificate":   auth.WithTLSState(context.Background(), &tls.ConnectionState{}),
		"no verifier":      auth.WithTLSState(context.Background(), presented),
		"verifier refuses": auth.WithPeerChainVerifier(auth.WithTLSState(context.Background(), presented), &clientCertVerifier{err: errors.New("untrusted")}),
	} {
		t.Run(name, func(t *testing.T) {
			thumb, err := VerifiedClientCertThumbprint(ctx)
			var required *ClientCertRequiredError
			if !errors.As(err, &required) {
				t.Fatalf("err = %v, want *ClientCertRequiredError", err)
			}
			if thumb != "" {
				t.Fatalf("thumbprint = %q on refusal", thumb)
			}
		})
	}
}

func TestVerifiedClientCertThumbprint_BindsTheVerifiedLeaf(t *testing.T) {
	cert := selfSignedClientCert(t)
	want := mtls.Thumbprint(cert.Raw)

	t.Run("tls verified chain", func(t *testing.T) {
		ctx := auth.WithTLSState(context.Background(), &tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{cert},
			VerifiedChains:   [][]*x509.Certificate{{cert}},
		})
		if got, err := VerifiedClientCertThumbprint(ctx); err != nil || got != want {
			t.Fatalf("thumbprint = %q, %v; want %q", got, err, want)
		}
	})
	t.Run("listener verifier", func(t *testing.T) {
		v := &clientCertVerifier{}
		ctx := auth.WithPeerChainVerifier(auth.WithTLSState(context.Background(), &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}), v)
		if got, err := VerifiedClientCertThumbprint(ctx); err != nil || got != want {
			t.Fatalf("thumbprint = %q, %v; want %q", got, err, want)
		}
		if v.calls != 1 {
			t.Fatalf("verifier calls = %d, want 1", v.calls)
		}
	})
}

func TestVerifiedClientCertThumbprint_TrustStateUnavailablePassesThrough(t *testing.T) {
	ctx := auth.WithPeerChainVerifier(
		auth.WithTLSState(context.Background(), &tls.ConnectionState{PeerCertificates: []*x509.Certificate{selfSignedClientCert(t)}}),
		&clientCertVerifier{err: unreadTrustError{}},
	)
	_, err := VerifiedClientCertThumbprint(ctx)
	var unavailable *auth.TrustStateUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("err = %v, want *auth.TrustStateUnavailableError", err)
	}
	var required *ClientCertRequiredError
	if errors.As(err, &required) {
		t.Fatal("a trust-state failure was reported as a missing certificate")
	}
}

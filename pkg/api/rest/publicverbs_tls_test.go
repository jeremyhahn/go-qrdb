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

package rest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/publicverbs"
)

// selfSignedClientCert issues a client certificate that chains to no CA
// the server trusts.
func selfSignedClientCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(20),
		Subject:      pkix.Name{CommonName: "mallory"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// TestPublicVerbsParity_RESTBadCertificateIsHandshakeFailure: on a listener
// that verifies a client certificate when one is given, a certificate that
// does not chain to the client CA ends the TLS handshake. It is never
// downgraded to an anonymous connection, so even the no-login GET /health
// fails, while the same request with no certificate is served.
func TestPublicVerbsParity_RESTBadCertificateIsHandshakeFailure(t *testing.T) {
	tlsCfg, pool := generateTestTLSConfig(t)
	serverCfg := tlsCfg.Clone()
	serverCfg.ClientAuth = tls.VerifyClientCertIfGiven
	serverCfg.ClientCAs = pool
	srv := NewServer(&mockKVService{}, &mockAdminService{}, dualStackAddrOn(t, "127.0.0.1"), WithTLS(serverCfg))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)

	_, path := publicverbs.RESTRoute(publicverbs.VerbHealth)
	url := "https://" + srv.Addr() + path
	get := func(cert *tls.Certificate) (*http.Response, error) {
		cfg := &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS13}
		if cert != nil {
			// Present cert whatever CAs the server names; crypto/tls would
			// otherwise withhold a certificate no named CA issued.
			cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return cert, nil }
		}
		tr := &http.Transport{TLSClientConfig: cfg}
		t.Cleanup(tr.CloseIdleConnections)
		client := &http.Client{Transport: tr, Timeout: 10 * time.Second}
		return client.Get(url)
	}

	resp, err := get(nil)
	if err != nil {
		t.Fatalf("GET %s with no certificate: %v", path, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s with no certificate = %d, want 200", path, resp.StatusCode)
	}

	foreign := selfSignedClientCert(t)
	resp, err = get(&foreign)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("GET %s with an unverifiable certificate = %d, want a handshake failure", path, resp.StatusCode)
	}
	// RFC 8446 section 4.4.2.4: the server aborts with a certificate
	// alert, which crypto/tls reports to the client as a "remote error".
	var opErr *net.OpError
	if !errors.As(err, &opErr) || opErr.Op != "remote error" {
		t.Fatalf("GET %s with an unverifiable certificate: err = %v, want the server's TLS alert", path, err)
	}
}

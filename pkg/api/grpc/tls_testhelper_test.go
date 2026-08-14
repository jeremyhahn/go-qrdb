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

package grpc

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc/credentials"
)

// testServerTLSConfig generates a self-signed ECDSA P-256 server TLS config
// valid for localhost, 127.0.0.1, and ::1, pinned to TLS 1.3. The gRPC
// transport is TLS-only (Start fails closed without a config), so every
// round-trip test that starts a real server uses this to secure the listener.
// The RootCAs pool is populated with the leaf so testClientCreds can build a
// matching, verifying client credential.
func testServerTLSConfig(t *testing.T) *tls.Config {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("generate ECDSA key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}

	certDER, err := x509.CreateCertificate(crand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	parsedCert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(parsedCert)

	return &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{certDER},
			PrivateKey:  key,
		}},
		RootCAs:    pool,
		MinVersion: tls.VersionTLS13,
	}
}

// testClientCreds builds gRPC transport credentials that trust the server
// certificate in serverCfg and verify it under the "localhost" server name,
// so a client dialing a "127.0.0.1:<port>" listener still validates the leaf.
func testClientCreds(t *testing.T, serverCfg *tls.Config) credentials.TransportCredentials {
	t.Helper()
	return credentials.NewTLS(&tls.Config{
		RootCAs:    serverCfg.RootCAs,
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
	})
}

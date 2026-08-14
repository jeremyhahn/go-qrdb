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
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// sharedGRPCServerTLS is a self-signed ECDSA P-256 server TLS config, generated
// once for the whole package, valid for localhost / 127.0.0.1 / ::1 and pinned
// to TLS 1.3. The gRPC transport is TLS-only (its Start fails closed without a
// config), so the shared round-trip helpers secure their listener with this
// config and their clients trust the same leaf via sharedGRPCClientTLS.
var sharedGRPCServerTLS, sharedGRPCClientTLS = mustSharedGRPCTLS()

// mustSharedGRPCTLS builds the shared server and client TLS configs. It panics
// on failure because it runs during package variable initialization, before
// any test's *testing.T exists.
func mustSharedGRPCTLS() (server, client *tls.Config) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		panic("grpc test TLS: generate key: " + err.Error())
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}

	certDER, err := x509.CreateCertificate(crand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic("grpc test TLS: create certificate: " + err.Error())
	}
	parsedCert, err := x509.ParseCertificate(certDER)
	if err != nil {
		panic("grpc test TLS: parse certificate: " + err.Error())
	}

	pool := x509.NewCertPool()
	pool.AddCert(parsedCert)

	server = &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{certDER},
			PrivateKey:  key,
		}},
		MinVersion: tls.VersionTLS13,
	}
	client = &tls.Config{
		RootCAs:    pool,
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
	}
	return server, client
}

// sharedGRPCClientTLSOption returns the SDK transport option that makes a client
// trust sharedGRPCServerTLS, verifying the server leaf under the "localhost"
// server name so a "127.0.0.1:<port>" dial still validates the certificate.
func sharedGRPCClientTLSOption() transport.Option {
	return transport.WithTLSConfig(sharedGRPCClientTLS)
}

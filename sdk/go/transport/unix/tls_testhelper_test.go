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

package unix

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

// testUnixServerTLS is a self-signed ECDSA P-256 server TLS config, generated
// once for the whole package, valid for localhost / 127.0.0.1 / ::1 and pinned
// to TLS 1.3. The Unix transport is TLS-only (its Start fails closed without a
// config), so every SDK round-trip test secures its listener with this config
// and its client trusts the same leaf via testClientTLSOption.
var testUnixServerTLS = mustSelfSignedTLS()

// mustSelfSignedTLS builds testUnixServerTLS. It panics on failure because it
// runs during package variable initialization, before any test's *testing.T
// exists.
func mustSelfSignedTLS() *tls.Config {
	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		panic("unix test TLS: generate key: " + err.Error())
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
		panic("unix test TLS: create certificate: " + err.Error())
	}
	parsedCert, err := x509.ParseCertificate(certDER)
	if err != nil {
		panic("unix test TLS: parse certificate: " + err.Error())
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

// testClientTLSOption returns the SDK transport option that makes a client
// trust testUnixServerTLS, verifying the server leaf under the "localhost"
// server name.
func testClientTLSOption() transport.Option {
	return transport.WithTLSConfig(&tls.Config{
		RootCAs:    testUnixServerTLS.RootCAs,
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
	})
}

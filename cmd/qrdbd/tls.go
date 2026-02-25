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

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"time"
)

// TLSBundle holds the CA and leaf certificate material generated for a
// node. It provides both the raw PEM bytes (for passing to the QUIC
// transport layer as MTLSConfig) and the parsed tls.Config (for the
// admin HTTP server).
type TLSBundle struct {
	// CACertPEM is the PEM-encoded CA certificate.
	CACertPEM []byte

	// CertPEM is the PEM-encoded leaf certificate signed by the CA.
	CertPEM []byte

	// KeyPEM is the PEM-encoded private key for the leaf certificate.
	KeyPEM []byte

	// TLSConfig is the parsed TLS configuration for the admin HTTP
	// server, using the leaf certificate and TLS 1.3.
	TLSConfig *tls.Config

	// CACertPool contains the CA certificate for verifying peer
	// certificates in HTTP clients and mTLS handshakes.
	CACertPool *x509.CertPool
}

// generateCATLS creates an ephemeral ECDSA P-256 CA and a leaf
// certificate signed by that CA. The leaf is valid for 24 hours with
// SANs for all local hostnames and interface IPs. ExtKeyUsage
// includes both ServerAuth and ClientAuth for mTLS.
// localSANs discovers all local DNS names and IP addresses suitable for
// TLS certificate Subject Alternative Names. It always includes localhost,
// 127.0.0.1, ::1, and 0.0.0.0, and additionally adds the machine hostname
// and all unicast interface IPs. This ensures certificates are valid when
// connections resolve to any local address (e.g. inside Docker containers).
func localSANs() (dnsNames []string, ips []net.IP) {
	dnsNames = []string{"localhost"}
	ips = []net.IP{
		net.IPv4(127, 0, 0, 1),
		net.IPv6loopback,
		net.IPv4zero,
		net.IPv6zero,
	}

	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		dnsNames = append(dnsNames, hostname)
	}

	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
				ips = append(ips, ipNet.IP)
			}
		}
	}

	return dnsNames, ips
}

func generateCATLS() (*TLSBundle, error) {
	// Generate CA key pair.
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, &TLSGenerateError{Err: err}
	}

	caSerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, &TLSGenerateError{Err: err}
	}

	caTemplate := &x509.Certificate{
		SerialNumber: caSerial,
		Subject: pkix.Name{
			Organization: []string{"go-quicraft"},
			CommonName:   "qrdb-ca",
		},
		NotBefore:             time.Now().Add(-1 * time.Minute),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}

	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, &TLSGenerateError{Err: err}
	}

	caCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertDER})

	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		return nil, &TLSGenerateError{Err: err}
	}

	// Generate leaf key pair.
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, &TLSGenerateError{Err: err}
	}

	leafSerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, &TLSGenerateError{Err: err}
	}

	dnsNames, ipAddrs := localSANs()

	leafTemplate := &x509.Certificate{
		SerialNumber: leafSerial,
		Subject: pkix.Name{
			Organization: []string{"go-quicraft"},
			CommonName:   "qrdb-node",
		},
		DNSNames:    dnsNames,
		IPAddresses: ipAddrs,
		NotBefore: time.Now().Add(-1 * time.Minute),
		NotAfter:  time.Now().Add(24 * time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
		BasicConstraintsValid: true,
		IsCA:                  false,
	}

	leafCertDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, &TLSGenerateError{Err: err}
	}

	leafKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return nil, &TLSGenerateError{Err: err}
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafCertDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: leafKeyDER})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, &TLSGenerateError{Err: err}
	}

	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	return &TLSBundle{
		CACertPEM:  caCertPEM,
		CertPEM:    certPEM,
		KeyPEM:     keyPEM,
		CACertPool: pool,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS13,
			NextProtos:   []string{"h3", "h2", "http/1.1"},
		},
	}, nil
}

// loadTLSFromFiles loads TLS certificate material from the given file
// paths and returns a TLSBundle. The caFile provides the CA certificate
// for peer verification; certFile and keyFile provide the leaf cert and
// key for this node.
func loadTLSFromFiles(caFile, certFile, keyFile string) (*TLSBundle, error) {
	caCertPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, &TLSLoadError{Path: caFile, Err: err}
	}

	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, &TLSLoadError{Path: certFile, Err: err}
	}

	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, &TLSLoadError{Path: keyFile, Err: err}
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, &TLSLoadError{Path: certFile, Err: err}
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caCertPEM) {
		return nil, &TLSLoadError{Path: caFile, Err: errNoCertsInPEM}
	}

	return &TLSBundle{
		CACertPEM:  caCertPEM,
		CertPEM:    certPEM,
		KeyPEM:     keyPEM,
		CACertPool: pool,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS13,
			NextProtos:   []string{"h3", "h2", "http/1.1"},
		},
	}, nil
}

// loadCACertPool loads a CA certificate pool from the given PEM file
// path. When path is empty, nil is returned (the caller should use the
// system certificate pool or skip verification). This is used by CLI
// commands like status and peers that only need to verify the server
// certificate, not present a client certificate.
func loadCACertPool(path string) (*x509.CertPool, error) {
	if path == "" {
		return nil, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &TLSLoadError{Path: path, Err: err}
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, &TLSLoadError{Path: path, Err: errNoCertsInPEM}
	}

	return pool, nil
}

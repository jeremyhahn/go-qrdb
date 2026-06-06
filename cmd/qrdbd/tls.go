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
	"errors"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	// caValidity is the default validity duration for auto-generated CA
	// certificates. 10 years provides a long-lived root of trust that
	// does not require frequent rotation.
	caValidity = 10 * 365 * 24 * time.Hour

	// nodeValidity is the default validity duration for auto-generated
	// node (leaf) certificates.
	nodeValidity = 365 * 24 * time.Hour

	// adminValidity is the default validity duration for auto-generated
	// admin client certificates.
	adminValidity = 365 * 24 * time.Hour
)

// TLSBundle holds the CA and leaf certificate material generated for a
// node. It provides both the raw PEM bytes (for passing to the QUIC
// transport layer as MTLSConfig) and the parsed tls.Config (for the
// admin HTTP server).
type TLSBundle struct {
	// CACertPEM is the PEM-encoded CA certificate.
	CACertPEM []byte

	// CAKeyPEM is the PEM-encoded CA private key. Only populated for
	// auto-generated certificates so that additional client certificates
	// (e.g., admin bootstrap cert) can be signed.
	CAKeyPEM []byte

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

	// Reloader provides atomic TLS certificate hot-reload. When non-nil,
	// the TLSConfig.GetCertificate callback is wired to use this reloader.
	Reloader *CertReloader
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

// generateCA creates a self-signed ECDSA P-256 CA certificate with the
// given validity duration. It returns the PEM-encoded certificate and key,
// the parsed certificate, and the private key so callers can sign leaf
// certificates.
func generateCA(validity time.Duration) (caCertPEM, caKeyPEM []byte, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, err error) {
	caKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, nil, &TLSGenerateError{Err: err}
	}

	caSerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, nil, nil, &TLSGenerateError{Err: err}
	}

	caTemplate := &x509.Certificate{
		SerialNumber: caSerial,
		Subject: pkix.Name{
			Organization: []string{"go-quicraft"},
			CommonName:   "qrdb-ca",
		},
		NotBefore:             time.Now().Add(-1 * time.Minute),
		NotAfter:              time.Now().Add(validity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}

	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, nil, &TLSGenerateError{Err: err}
	}

	caCertPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertDER})

	caKeyDER, err := x509.MarshalECPrivateKey(caKey)
	if err != nil {
		return nil, nil, nil, nil, &TLSGenerateError{Err: err}
	}
	caKeyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: caKeyDER})

	caCert, err = x509.ParseCertificate(caCertDER)
	if err != nil {
		return nil, nil, nil, nil, &TLSGenerateError{Err: err}
	}

	return caCertPEM, caKeyPEM, caCert, caKey, nil
}

// generateNodeCert creates an ECDSA P-256 leaf certificate signed by the
// given CA. The leaf includes SANs for all local hostnames and interface
// IPs. ExtKeyUsage includes both ServerAuth and ClientAuth for mTLS.
func generateNodeCert(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, validity time.Duration) (certPEM, keyPEM []byte, err error) {
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, &TLSGenerateError{Err: err}
	}

	leafSerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, &TLSGenerateError{Err: err}
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
		NotBefore:   time.Now().Add(-1 * time.Minute),
		NotAfter:    time.Now().Add(validity),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
		BasicConstraintsValid: true,
		IsCA:                  false,
	}

	leafCertDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, &TLSGenerateError{Err: err}
	}

	leafKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return nil, nil, &TLSGenerateError{Err: err}
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafCertDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: leafKeyDER})

	return certPEM, keyPEM, nil
}

// generateClientCert creates an ECDSA P-256 client certificate signed
// by the given CA. CN is set to cn and OU to ou. The certificate has
// only ExtKeyUsageClientAuth so it cannot be used for server
// authentication.
func generateClientCert(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, cn, ou string, validity time.Duration) (certPEM, keyPEM []byte, err error) {
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, &TLSGenerateError{Err: err}
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, &TLSGenerateError{Err: err}
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization:       []string{"go-quicraft"},
			CommonName:         cn,
			OrganizationalUnit: []string{ou},
		},
		NotBefore: time.Now().Add(-1 * time.Minute),
		NotAfter:  time.Now().Add(validity),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageClientAuth,
		},
		BasicConstraintsValid: true,
		IsCA:                  false,
	}

	clientCertDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &clientKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, &TLSGenerateError{Err: err}
	}

	clientKeyDER, err := x509.MarshalECPrivateKey(clientKey)
	if err != nil {
		return nil, nil, &TLSGenerateError{Err: err}
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientCertDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: clientKeyDER})

	return certPEM, keyPEM, nil
}

func generateCATLS() (*TLSBundle, error) {
	caCertPEM, caKeyPEM, caCert, caKey, err := generateCA(caValidity)
	if err != nil {
		return nil, err
	}

	certPEM, keyPEM, err := generateNodeCert(caCert, caKey, nodeValidity)
	if err != nil {
		return nil, err
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, &TLSGenerateError{Err: err}
	}

	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	return &TLSBundle{
		CACertPEM:  caCertPEM,
		CAKeyPEM:   caKeyPEM,
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
	return loadTLSFromFilesWithExtra(caFile, certFile, keyFile, nil, nil)
}

// loadTLSFromFilesWithExtra loads TLS certificate material from the
// supplied file paths and merges any additional CA pools into the
// returned bundle's CACertPool. additionalCAs is layered onto the
// server CA pool used for peer verification (e.g.,
// cfg.TLS.AdditionalCAs); clientCAFiles is reserved for client
// certificate verification (e.g., cfg.MTLS.ClientCAFiles) and is
// merged into the same pool because the bundle currently exposes a
// single CertPool consumed by both peer and client verification paths.
// All paths flow through loadCAFilesUnion which deduplicates by
// canonical filename.
func loadTLSFromFilesWithExtra(caFile, certFile, keyFile string, additionalCAs, clientCAFiles []string) (*TLSBundle, error) {
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

	// Layer in the union of additional and mTLS client CAs. The helper
	// canonicalizes each path so the primary CA (already loaded above)
	// and any duplicates across the two slices are de-duped.
	union := append([]string{caFile}, additionalCAs...)
	union = append(union, clientCAFiles...)
	extra, err := loadCAFilesUnion(union)
	if err != nil {
		return nil, err
	}
	if extra != nil {
		// Re-build the pool from the union so that all certs are
		// present in a single deduplicated pool. The primary CA is
		// included in the union list so its trust survives.
		pool = extra
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

// loadCAFilesUnion loads the union of CA PEM files from the supplied
// paths into a single x509.CertPool. Paths are deduplicated by their
// canonical (absolute, symlink-evaluated) filename so that the same CA
// referenced from multiple input slices is parsed only once. An empty
// or nil paths slice yields a nil pool with no error so callers can
// distinguish "no additional trust" from a hard failure. Each PEM file
// must contain at least one CERTIFICATE block; missing files and
// malformed PEM data return a *TLSLoadError.
func loadCAFilesUnion(paths []string) (*x509.CertPool, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	pool := x509.NewCertPool()
	seen := make(map[string]struct{}, len(paths))
	added := false

	for _, raw := range paths {
		if raw == "" {
			continue
		}
		canonical, err := canonicalizePath(raw)
		if err != nil {
			return nil, &TLSLoadError{Path: raw, Err: err}
		}
		if _, dup := seen[canonical]; dup {
			continue
		}
		seen[canonical] = struct{}{}

		// #nosec G304 - CA paths come from validated configuration.
		data, err := os.ReadFile(canonical)
		if err != nil {
			return nil, &TLSLoadError{Path: canonical, Err: err}
		}
		if !pool.AppendCertsFromPEM(data) {
			return nil, &TLSLoadError{Path: canonical, Err: errNoCertsInPEM}
		}
		added = true
	}

	if !added {
		return nil, nil
	}
	return pool, nil
}

// canonicalizePath converts a CA file path into a stable identifier
// suitable for filename-based deduplication. It evaluates symlinks
// when possible and falls back to filepath.Abs when the path is not
// yet present on disk so that callers receive a deterministic key
// even before the read attempt fails.
func canonicalizePath(path string) (string, error) {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		// EvalSymlinks fails on missing files; surface the absolute
		// path so deduplication still works and the subsequent
		// os.ReadFile call reports the underlying I/O error.
		if errors.Is(err, os.ErrNotExist) {
			return path, nil
		}
		return "", err
	}
	return resolved, nil
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

// generateAdminClientCert creates a client certificate for the admin user
// signed by the CA from the TLS bundle. CN is set to the admin username
// and OU to the tenant ID so that the mTLS authenticator can extract
// the identity. The cert and key are written to {dataDir}/admin-cert.pem
// and {dataDir}/admin-key.pem. If both files already exist, the function
// returns nil without regenerating.
func generateAdminClientCert(bundle *TLSBundle, dataDir, username, tenantID string) error {
	certPath := filepath.Join(dataDir, "admin-cert.pem")
	keyPath := filepath.Join(dataDir, "admin-key.pem")

	// Skip if both files already exist.
	if fileExists(certPath) && fileExists(keyPath) {
		slog.Info("admin client certificate already exists", "cert", certPath)
		return nil
	}

	// Parse the CA certificate from PEM.
	caBlock, _ := pem.Decode(bundle.CACertPEM)
	if caBlock == nil {
		return &AdminCertError{Err: errNoCertsInPEM}
	}
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		return &AdminCertError{Err: err}
	}

	// Parse the CA private key from PEM.
	if len(bundle.CAKeyPEM) == 0 {
		return &AdminCertError{Err: errNoCAKeyAvailable}
	}
	caKeyBlock, _ := pem.Decode(bundle.CAKeyPEM)
	if caKeyBlock == nil {
		return &AdminCertError{Err: errNoCAKeyAvailable}
	}
	caKey, err := x509.ParseECPrivateKey(caKeyBlock.Bytes)
	if err != nil {
		return &AdminCertError{Err: err}
	}

	clientCertPEM, clientKeyPEM, err := generateClientCert(caCert, caKey, username, tenantID, adminValidity)
	if err != nil {
		return &AdminCertError{Err: err}
	}

	// Write certificate and key files.
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return &AdminCertWriteError{Path: dataDir, Err: err}
	}
	if err := os.WriteFile(certPath, clientCertPEM, 0o600); err != nil {
		return &AdminCertWriteError{Path: certPath, Err: err}
	}
	if err := os.WriteFile(keyPath, clientKeyPEM, 0o600); err != nil {
		return &AdminCertWriteError{Path: keyPath, Err: err}
	}

	slog.Info("admin client certificate generated",
		"cert", certPath,
		"key", keyPath,
		"cn", username,
		"ou", tenantID,
	)
	return nil
}

// fileExists returns true when the path refers to an existing regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// loadTLSFromDir attempts to load TLS certificate material from the
// standard file names in dataDir: ca.pem, node-cert.pem, node-key.pem.
// If all three files exist they are loaded via loadTLSFromFiles. If the
// optional ca-key.pem is present it populates CAKeyPEM. When the
// required files do not exist, nil is returned without an error
// (indicating no cached certificates are available).
func loadTLSFromDir(dataDir string) (*TLSBundle, error) {
	caPath := filepath.Join(dataDir, "ca.pem")
	certPath := filepath.Join(dataDir, "node-cert.pem")
	keyPath := filepath.Join(dataDir, "node-key.pem")

	// All three files must be present; missing files mean no cached certs.
	if !fileExists(caPath) || !fileExists(certPath) || !fileExists(keyPath) {
		return nil, nil
	}

	bundle, err := loadTLSFromFiles(caPath, certPath, keyPath)
	if err != nil {
		return nil, err
	}

	// Optionally load the CA private key if available.
	caKeyPath := filepath.Join(dataDir, "ca-key.pem")
	if fileExists(caKeyPath) {
		caKeyPEM, err := os.ReadFile(caKeyPath)
		if err != nil {
			return nil, &TLSLoadError{Path: caKeyPath, Err: err}
		}
		bundle.CAKeyPEM = caKeyPEM
	}

	return bundle, nil
}

// persistFullBundle writes the TLS bundle to standard file names in
// dataDir: ca.pem, node-cert.pem, node-key.pem, and optionally
// ca-key.pem if CAKeyPEM is populated. The directory is created with
// 0o700 permissions and files with 0o600.
func persistFullBundle(bundle *TLSBundle, dataDir string) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return &BootstrapWriteError{Path: dataDir, Err: err}
	}

	caPath := filepath.Join(dataDir, "ca.pem")
	if err := os.WriteFile(caPath, bundle.CACertPEM, 0o600); err != nil {
		return &BootstrapWriteError{Path: caPath, Err: err}
	}

	certPath := filepath.Join(dataDir, "node-cert.pem")
	if err := os.WriteFile(certPath, bundle.CertPEM, 0o600); err != nil {
		return &BootstrapWriteError{Path: certPath, Err: err}
	}

	keyPath := filepath.Join(dataDir, "node-key.pem")
	if err := os.WriteFile(keyPath, bundle.KeyPEM, 0o600); err != nil {
		return &BootstrapWriteError{Path: keyPath, Err: err}
	}

	if len(bundle.CAKeyPEM) > 0 {
		caKeyPath := filepath.Join(dataDir, "ca-key.pem")
		if err := os.WriteFile(caKeyPath, bundle.CAKeyPEM, 0o600); err != nil {
			return &BootstrapWriteError{Path: caKeyPath, Err: err}
		}
	}

	return nil
}

// generateLeafFromCA parses concatenated CA certificate + CA private key
// PEM bytes (e.g., from a bootstrap endpoint) and generates a node leaf
// certificate signed by the CA. The returned TLSBundle is fully
// populated including TLSConfig and CACertPool.
func generateLeafFromCA(bundlePEM []byte) (*TLSBundle, error) {
	// Parse CA certificate.
	caBlock, rest := pem.Decode(bundlePEM)
	if caBlock == nil || caBlock.Type != "CERTIFICATE" {
		return nil, &BootstrapKeyError{Kind: "CA certificate", Err: errors.New("no CERTIFICATE block found in PEM data")}
	}
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		return nil, &BootstrapKeyError{Kind: "CA certificate", Err: err}
	}
	caCertPEM := pem.EncodeToMemory(caBlock)

	// Parse CA private key.
	keyBlock, _ := pem.Decode(rest)
	if keyBlock == nil || keyBlock.Type != "EC PRIVATE KEY" {
		return nil, &BootstrapKeyError{Kind: "CA private key", Err: errors.New("no EC PRIVATE KEY block found in PEM data")}
	}
	caKey, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, &BootstrapKeyError{Kind: "CA private key", Err: err}
	}
	caKeyPEM := pem.EncodeToMemory(keyBlock)

	// Generate the node leaf certificate.
	certPEM, keyPEM, err := generateNodeCert(caCert, caKey, nodeValidity)
	if err != nil {
		return nil, err
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, &TLSGenerateError{Err: err}
	}

	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	return &TLSBundle{
		CACertPEM:  caCertPEM,
		CAKeyPEM:   caKeyPEM,
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

// wireCertReloader creates a CertReloader for the given certificate and
// key paths, then wires it into the bundle's TLSConfig so that the
// GetCertificate callback atomically serves the latest certificate from
// disk. The static Certificates slice is cleared so GetCertificate
// takes precedence. The reloader is stored on the bundle for lifecycle
// management.
func wireCertReloader(bundle *TLSBundle, certPath, keyPath string, logger *slog.Logger) error {
	reloader, err := NewCertReloader(certPath, keyPath, logger)
	if err != nil {
		return err
	}

	bundle.TLSConfig.GetCertificate = reloader.GetCertificate
	bundle.TLSConfig.Certificates = nil
	bundle.Reloader = reloader

	return nil
}

// errNoCAKeyAvailable is returned when the TLS bundle does not contain a
// CA private key, which is required for signing additional certificates.
var errNoCAKeyAvailable = errors.New("CA private key not available in TLS bundle")

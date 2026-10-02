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
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
)

const (
	// caValidity is the default validity duration for auto-generated CA
	// certificates.
	caValidity = clustertls.CAValidity

	// nodeValidity is the default validity duration for auto-generated
	// node (leaf) certificates.
	nodeValidity = clustertls.NodeValidity

	// adminValidity is the default validity duration for auto-generated
	// admin client certificates.
	adminValidity = clustertls.ClientValidity
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

	// CACertPool is the server-trust pool: it verifies the certificates that
	// peer servers present (used as RootCAs by leader-forwarding and cluster
	// key-transport clients). It is the primary CA plus any operator-supplied
	// additional peer CAs.
	CACertPool *x509.CertPool

	// ClientCACertPool is the client-auth trust pool: it verifies the
	// certificates that clients present (used as ClientCAs on the listener). It
	// is the primary CA plus any operator-supplied client CAs, kept separate
	// from CACertPool so a CA trusted only to verify server peers cannot also
	// mint accepted client identities (and vice versa). For self-signed cluster
	// material both pools hold the same single CA.
	ClientCACertPool *x509.CertPool

	// Reloader provides atomic TLS certificate hot-reload. When non-nil,
	// the TLSConfig.GetCertificate callback is wired to use this reloader.
	Reloader *CertReloader

	// ExplicitTLS is true only for resolveTLS's path 1 (--tls-ca-file,
	// --tls-cert-file and --tls-key-file all supplied): CACertPEM is then
	// an operator-supplied trust anchor qrdb was never handed the private
	// key for, not bootstrap material for the cluster CA (Key A). It is
	// false for every other path, including path 2 (cached certs in
	// data-dir), which is this same node's own earlier self-issued
	// material and must still be offered to the cluster CA provider. See
	// docs/certificate-authority/architecture.md, "server TLS identity and
	// the cluster/login CA are separate concerns".
	ExplicitTLS bool

	// keyBProvider is the CertProvider wireClientTrustPool's
	// GetConfigForClient hook reads Key B's CA certificates from, set once
	// post-unseal via SetKeyBProvider. nil (the state before that call, and
	// again after a reseal that makes the provider's own calls fail) means
	// the dynamic trust pool holds Key A only.
	keyBProvider atomic.Pointer[keyBProviderBox]
}

// keyBProviderBox boxes a cert.CertProvider so it can be held in an
// atomic.Pointer: that type needs a concrete pointee, and an interface value
// is not one.
type keyBProviderBox struct {
	prov cert.CertProvider
}

// keyBCertSource is the capability DefaultCertProvider.CACertPEMs provides:
// every tenant's Key B CA certificate (or, with an injected cakey.Provider,
// the single one that replaces them). It is declared here, rather than
// added to cert.CertProvider, because that interface is what an embedder's
// own CertProvider replacement (server.WithCertProvider) must satisfy, and
// multi-tenant enumeration is not part of its contract -- a type assertion
// against this shape is how the trust pool asks for the capability without
// widening the interface every CertProvider implementation must carry.
type keyBCertSource interface {
	CACertPEMs(ctx context.Context) ([][]byte, error)
}

// SetKeyBProvider installs the CertProvider the dynamic client-auth trust
// pool (wireClientTrustPool) reads Key B's CA certificates from. Call it
// once, as soon as the CertProvider exists -- which is after
// lifecycle.BuildServices runs, meaning after the barrier is first unsealed.
// Safe to call concurrently with a live GetConfigForClient hook: the next
// connection sees the newly installed provider.
func (b *TLSBundle) SetKeyBProvider(p cert.CertProvider) {
	b.keyBProvider.Store(&keyBProviderBox{prov: p})
}

// wireClientTrustPool installs TLSConfig.GetConfigForClient, the dynamic
// client-auth trust pool every client-facing listener sharing this bundle's
// TLSConfig pointer (REST, gRPC, Unix, QUIC, MCP) verifies a presented
// client certificate against: Key A (ClientCACertPool, the static pool
// already assigned to TLSConfig.ClientCAs) plus every tenant's Key B CA
// certificate, read fresh from whatever provider SetKeyBProvider most
// recently installed. See docs/certificate-authority/architecture.md, "Key B
// trust on the client-facing listeners".
//
// Call once, before any listener using this bundle starts (including the
// pre-host sealed-window listener), so the hook is live even before
// SetKeyBProvider has ever been called -- at that point it returns (nil,
// nil), which keeps crypto/tls on the original, static config (Key A only),
// exactly the pre-unseal and post-reseal behavior the architecture
// prescribes.
//
// The peer/Raft/enrollment transport never shares this bundle's TLSConfig:
// it builds its own trust pool directly from CACertPEM (Key A only -- see
// quicraftconfig.MTLSConfig in start.go), so a Key B leaf is refused there
// regardless of what this pool trusts.
func (b *TLSBundle) wireClientTrustPool() {
	b.TLSConfig.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		box := b.keyBProvider.Load()
		if box == nil || box.prov == nil {
			return nil, nil
		}
		src, ok := box.prov.(keyBCertSource)
		if !ok {
			return nil, nil
		}
		pems, err := src.CACertPEMs(context.Background())
		if err != nil || len(pems) == 0 {
			// Sealed (the ordinary cause in Raft mode, where the replicated
			// store cannot be opened while sealed), or no tenant has
			// established Key B yet: fall back to the static config, not an
			// error, so a Key A holder keeps authenticating through a
			// reseal.
			return nil, nil
		}

		pool := b.ClientCACertPool.Clone()
		for _, p := range pems {
			pool.AppendCertsFromPEM(p)
		}
		clone := b.TLSConfig.Clone()
		clone.ClientCAs = pool
		// crypto/tls invokes GetConfigForClient at most once per handshake,
		// so this is not re-entered for the connection being set up; nil it
		// anyway so the clone is never mistaken for a second, live hook.
		clone.GetConfigForClient = nil
		return clone, nil
	}
}

// localSANs discovers all local DNS names and IP addresses suitable for
// TLS certificate Subject Alternative Names. It always includes localhost,
// 127.0.0.1, and ::1, and additionally adds the machine hostname and all
// non-loopback unicast interface IPs.
func localSANs() (dnsNames []string, ips []net.IP) {
	return clustertls.LocalSANs()
}

// generateCA creates a self-signed ECDSA P-256 CA certificate with the
// given validity duration.
func generateCA(validity time.Duration) (caCertPEM, caKeyPEM []byte, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, err error) {
	return clustertls.GenerateCA(validity)
}

// generateNodeCert creates an ECDSA P-256 leaf certificate signed by the
// given CA with SANs for all local hostnames and interface IPs. When nodeURI
// is provided and non-empty it is embedded as a URI SAN.
func generateNodeCert(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, validity time.Duration, nodeURI ...string) (certPEM, keyPEM []byte, err error) {
	return clustertls.GenerateNodeCert(caCert, caKey, validity, nodeURI...)
}

// generateClientCert creates an ECDSA P-256 client certificate signed by the
// given CA. CN is set to cn and OU to ou.
func generateClientCert(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, cn, ou string, validity time.Duration) (certPEM, keyPEM []byte, err error) {
	return clustertls.GenerateClientCert(caCert, caKey, cn, ou, validity)
}

// generateCATLS creates an ephemeral ECDSA P-256 CA and a leaf certificate
// signed by that CA. nodeURI is an optional SPIFFE node URI SAN (see
// generateNodeCert); callers that do not need the SAN omit the argument.
func generateCATLS(nodeURI ...string) (*TLSBundle, error) {
	m, err := clustertls.GenerateCABundle(nodeURI...)
	if err != nil {
		return nil, err
	}
	return tlsBundleFromMaterial(m)
}

// loadTLSFromDir attempts to load TLS certificate material from the
// standard file names in dataDir: ca.pem, node-cert.pem, node-key.pem.
// If all three files exist they are loaded via loadTLSFromFiles. If the
// optional ca-key.pem is present it populates CAKeyPEM. When the
// required files do not exist, nil is returned without an error
// (indicating no cached certificates are available).
func loadTLSFromDir(dataDir string) (*TLSBundle, error) {
	m, err := clustertls.LoadFromDir(dataDir)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, nil
	}
	// Re-use the admin-specific file loader to build the parsed tls.Config and
	// the deduplicated CA cert pool that qrdbd's admin HTTP server requires.
	caPath := filepath.Join(dataDir, "ca.pem")
	certPath := filepath.Join(dataDir, "node-cert.pem")
	keyPath := filepath.Join(dataDir, "node-key.pem")
	bundle, err := loadTLSFromFiles(caPath, certPath, keyPath)
	if err != nil {
		return nil, err
	}
	bundle.CAKeyPEM = m.CAKeyPEM
	return bundle, nil
}

// tlsBundleFromMaterial assembles a qrdbd TLSBundle from raw clustertls
// Material by parsing the key pair and building the CA cert pool for the
// admin HTTP server.
func tlsBundleFromMaterial(m *clustertls.Material) (*TLSBundle, error) {
	cert, err := tls.X509KeyPair(m.CertPEM, m.KeyPEM)
	if err != nil {
		return nil, &TLSGenerateError{Err: err}
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(m.CACertPEM) {
		return nil, &TLSGenerateError{Err: errors.New("no certificates in CA PEM material")}
	}

	return &TLSBundle{
		CACertPEM:        m.CACertPEM,
		CAKeyPEM:         m.CAKeyPEM,
		CertPEM:          m.CertPEM,
		KeyPEM:           m.KeyPEM,
		CACertPool:       pool,
		ClientCACertPool: pool, // self-signed cluster CA verifies both roles
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
// supplied file paths and returns a bundle with two distinct trust pools.
// additionalCAs is layered onto CACertPool, the server-trust pool used as
// RootCAs for peer verification (e.g. cfg.TLS.AdditionalCAs); clientCAFiles
// is layered onto ClientCACertPool, the client-auth pool used as ClientCAs
// (e.g. cfg.MTLS.ClientCAFiles). The two pools are kept separate so a CA
// trusted for one role is not silently trusted for the other; the primary
// caFile seeds both. All paths flow through loadCAFilesUnion which
// deduplicates by canonical filename.
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

	// Server-trust pool: the primary CA plus any additional peer-trust CAs
	// (cfg.TLS.AdditionalCAs). Used as RootCAs when this node verifies the
	// certificate a peer server presents.
	serverPool := x509.NewCertPool()
	if !serverPool.AppendCertsFromPEM(caCertPEM) {
		return nil, &TLSLoadError{Path: caFile, Err: errNoCertsInPEM}
	}
	if extra, err := loadCAFilesUnion(append([]string{caFile}, additionalCAs...)); err != nil {
		return nil, err
	} else if extra != nil {
		serverPool = extra
	}

	// Client-auth pool: the primary CA plus any dedicated client-auth CAs
	// (cfg.MTLS.ClientCAFiles). Used as ClientCAs when this node verifies the
	// certificate a client presents. Kept separate from serverPool so a CA
	// trusted only for one role cannot be used for the other.
	clientPool := x509.NewCertPool()
	if !clientPool.AppendCertsFromPEM(caCertPEM) {
		return nil, &TLSLoadError{Path: caFile, Err: errNoCertsInPEM}
	}
	if extra, err := loadCAFilesUnion(append([]string{caFile}, clientCAFiles...)); err != nil {
		return nil, err
	} else if extra != nil {
		clientPool = extra
	}

	return &TLSBundle{
		CACertPEM:        caCertPEM,
		CertPEM:          certPEM,
		KeyPEM:           keyPEM,
		CACertPool:       serverPool,
		ClientCACertPool: clientPool,
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

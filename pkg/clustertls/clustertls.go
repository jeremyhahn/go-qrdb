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

// Package clustertls is the shared cluster-TLS certificate material generation
// and directory cache toolkit. It is consumed by cmd/qrdbd and go-xkms to
// generate and persist raw PEM material (CA + leaf) for mutual TLS cluster
// communication. It provides cert material generation and directory-based
// caching only; it does NOT perform Noise/truststrap orchestration — that
// responsibility stays per-consumer.
package clustertls

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/util/securefile"
)

const (
	// CAValidity is the default validity duration for auto-generated CA
	// certificates. 10 years provides a long-lived root of trust that does
	// not require frequent rotation.
	CAValidity = 10 * 365 * 24 * time.Hour

	// NodeValidity is the default validity duration for auto-generated node
	// (leaf) certificates.
	NodeValidity = 365 * 24 * time.Hour

	// ClientValidity is the default validity duration for auto-generated admin
	// client certificates.
	ClientValidity = 365 * 24 * time.Hour
)

// Material holds the raw PEM-encoded certificate and key bytes for a cluster
// CA + node leaf certificate pair. It carries no parsed crypto objects and no
// tls.Config or x509.CertPool; callers assemble those from the PEM bytes.
type Material struct {
	// CACertPEM is the PEM-encoded CA certificate.
	CACertPEM []byte

	// CAKeyPEM is the PEM-encoded CA private key. Only populated for
	// auto-generated certificates so that additional leaf certificates can
	// be signed without re-reading from disk.
	CAKeyPEM []byte

	// CertPEM is the PEM-encoded leaf certificate signed by the CA.
	CertPEM []byte

	// KeyPEM is the PEM-encoded private key for the leaf certificate.
	KeyPEM []byte
}

// GenerateError is returned when TLS certificate generation fails.
type GenerateError struct {
	Err error
}

// Error returns a human-readable description.
func (e *GenerateError) Error() string {
	return "clustertls: TLS certificate generation failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *GenerateError) Unwrap() error {
	return e.Err
}

// LoadError is returned when loading TLS certificate material from a file fails.
type LoadError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *LoadError) Error() string {
	return "clustertls: failed to load TLS file " + strconv.Quote(e.Path) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *LoadError) Unwrap() error {
	return e.Err
}

// KeyError is returned when parsing certificate or key material fails.
type KeyError struct {
	// Kind identifies which piece of material could not be parsed
	// (e.g. "CA certificate", "CA private key").
	Kind string
	Err  error
}

// Error returns a human-readable description.
func (e *KeyError) Error() string {
	return "clustertls: key parse failed (" + e.Kind + "): " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *KeyError) Unwrap() error {
	return e.Err
}

// WriteError is returned when persisting certificate material to disk fails.
type WriteError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *WriteError) Error() string {
	return "clustertls: failed to write file " + strconv.Quote(e.Path) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *WriteError) Unwrap() error {
	return e.Err
}

// ErrNoCertsInPEM is returned when a PEM file or buffer contains no valid
// certificate blocks.
var ErrNoCertsInPEM = errors.New("clustertls: no certificates found in PEM data")

// PartialMaterialError is returned by LoadFromDir when some but not all of the
// required cluster-TLS files are present in a data directory. This signals an
// interrupted write or a corrupted directory rather than a clean "no cache yet"
// state, so callers fail closed instead of regenerating a fresh CA.
type PartialMaterialError struct {
	// Dir is the data directory that was scanned.
	Dir string
	// Missing lists the absolute paths of the required files that are absent.
	Missing []string
}

// Error implements the error interface.
func (e *PartialMaterialError) Error() string {
	return "clustertls: incomplete TLS material in " + strconv.Quote(e.Dir) +
		": missing " + strings.Join(e.Missing, ", ")
}

// LocalSANs discovers all local DNS names and IP addresses suitable for TLS
// certificate Subject Alternative Names. It always includes localhost,
// 127.0.0.1, and ::1, and additionally adds the machine hostname and all
// non-loopback unicast interface IPs. The unspecified addresses 0.0.0.0 and ::
// are deliberately excluded: nothing connects to them, so they carry no
// verification value and only widen the cert's stated identity.
func LocalSANs() (dnsNames []string, ips []net.IP) {
	dnsNames = []string{"localhost"}
	ips = []net.IP{
		net.IPv4(127, 0, 0, 1),
		net.IPv6loopback,
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

// GenerateCA creates a self-signed ECDSA P-256 CA certificate with the given
// validity duration. It returns the PEM-encoded certificate and key, the
// parsed certificate, and the private key so callers can sign leaf certificates.
func GenerateCA(validity time.Duration) (caCertPEM, caKeyPEM []byte, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, err error) {
	caKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, nil, &GenerateError{Err: err}
	}

	caSerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, nil, nil, &GenerateError{Err: err}
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
		return nil, nil, nil, nil, &GenerateError{Err: err}
	}

	caCertPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertDER})

	caKeyDER, err := x509.MarshalECPrivateKey(caKey)
	if err != nil {
		return nil, nil, nil, nil, &GenerateError{Err: err}
	}
	caKeyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: caKeyDER})

	caCert, err = x509.ParseCertificate(caCertDER)
	if err != nil {
		return nil, nil, nil, nil, &GenerateError{Err: err}
	}

	return caCertPEM, caKeyPEM, caCert, caKey, nil
}

// NodeLeafTemplate builds the x509 certificate template used for a node's
// OWN cluster leaf certificate: GenerateNodeCert and the raw-PEM
// IssueLeafForKey below, both of which issue a leaf for the SAME node that
// is doing the issuing (an auto-generated first node, or a caller supplying
// its own hardware-resident key). The template therefore carries this
// node's own SANs -- all local hostnames and interface IPs, from LocalSANs
// -- alongside the DigitalSignature+KeyEncipherment key usages and both
// ServerAuth and ClientAuth extended key usages for mutual TLS. When
// nodeURI is provided and non-empty it is parsed as a URI and appended as a
// URI SAN so the node carries its SPIFFE identity. A non-empty URI that
// fails to parse returns a GenerateError.
//
// This is NOT the template for a leaf issued to a DIFFERENT node -- see
// IdentityLeafTemplate, used by member-side cluster join, where LocalSANs
// would carry the SIGNING member's own hostnames and IPs onto a certificate
// a completely different node is about to start serving traffic from.
func NodeLeafTemplate(validity time.Duration, nodeURI ...string) (*x509.Certificate, error) {
	dnsNames, ipAddrs := LocalSANs()
	return leafTemplate(validity, dnsNames, ipAddrs, nodeURI...)
}

// IdentityLeafTemplate builds the x509 certificate template for a leaf
// issued to ANOTHER node's key -- member-side cluster join's
// clusterca.IssueLeafForKey, reached through HandleJoinRequest on both
// transports. It carries NO DNS/IP SANs of the signing member's own: unlike
// NodeLeafTemplate, the certificate is for a node this process is not and
// has no address information for, so a signer's own hostnames and
// interface IPs on it would be actively wrong, not merely redundant. The
// joining node's identity is carried ONLY as the URI SAN nodeURI supplies
// (its SPIFFE node ID); peers verify this certificate by that identity, not
// by any SAN enumerating addresses. A non-empty URI that fails to parse
// returns a GenerateError.
// IdentityLeafTemplate builds the x509 certificate template for a leaf
// issued to ANOTHER node's key -- member-side cluster join's
// clusterca.IssueLeafForKey, reached through HandleJoinRequest on both
// transports. It carries no DNS/IP SANs of the SIGNING member's own: unlike
// NodeLeafTemplate, LocalSANs here would carry the signer's own hostnames and
// interface IPs onto a certificate a completely different node is about to
// start serving traffic from.
//
// It DOES carry the joining node's own address as a SAN, recovered from the
// SPIFFE node URI nodeURI itself supplies (spiffe://<trustDomain>/node/<addr>)
// via the same spiffe.ParseNodeID recovery clusterenroll.issueAndAdmit uses
// to key the pre-membership feature advertisement: classified as an
// IPAddress SAN when addr parses as an IP, a DNSName SAN otherwise. Without
// it, the certificate carries only the URI SAN, and go-quicraft's own Raft
// QUIC transport dials a peer by exactly this address and runs the standard
// x509 SAN check against it (go-quicraft/pkg/internal/transport/conn.go,
// dialAndPool, sets tls.Config.ServerName to the dial host before Dial) --
// there is no SPIFFE-aware substitute for that check anywhere in the
// transport, so a leaf with no matching SAN can never be dialed as a raft
// peer, only reached by a caller (like the admin HTTPS client) that skips
// the hostname check outright. A non-empty URI that fails to parse returns a
// GenerateError; one that parses but is not a node SPIFFE URI, or carries no
// recoverable address, yields no address SAN -- same as before this.
func IdentityLeafTemplate(validity time.Duration, nodeURI ...string) (*x509.Certificate, error) {
	dnsNames, ipAddrs, err := addressSANs(nodeURI...)
	if err != nil {
		return nil, err
	}
	return leafTemplate(validity, dnsNames, ipAddrs, nodeURI...)
}

// addressSANs recovers the address embedded in a node SPIFFE URI
// (spiffe://<trustDomain>/node/<addr>) and classifies it as an IP or DNS SAN
// for IdentityLeafTemplate. addr itself may carry a port (the raft address
// form every caller in this codebase uses); only the host portion is a valid
// SAN, so a port is split off first when present. A URI that is not a node
// SPIFFE URI, or carries no address, yields no SANs rather than an error --
// IdentityLeafTemplate's own URI SAN handling is what rejects a malformed
// URI.
func addressSANs(nodeURI ...string) ([]string, []net.IP, error) {
	if len(nodeURI) == 0 || nodeURI[0] == "" {
		return nil, nil, nil
	}
	u, err := url.Parse(nodeURI[0])
	if err != nil {
		return nil, nil, &GenerateError{Err: err}
	}
	identity, ok, err := spiffe.ParseNodeID(u)
	if err != nil || !ok {
		return nil, nil, nil
	}
	host := identity.NodeID
	if h, _, splitErr := net.SplitHostPort(host); splitErr == nil {
		host = h
	}
	if ip := net.ParseIP(host); ip != nil {
		return nil, []net.IP{ip}, nil
	}
	return []string{host}, nil, nil
}

// leafTemplate is the shared builder behind NodeLeafTemplate and
// IdentityLeafTemplate: identical certificate shape, differing only in
// whether dnsNames/ipAddrs carry the signer's own LocalSANs or nothing.
func leafTemplate(validity time.Duration, dnsNames []string, ipAddrs []net.IP, nodeURI ...string) (*x509.Certificate, error) {
	leafSerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, &GenerateError{Err: err}
	}

	template := &x509.Certificate{
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

	if len(nodeURI) > 0 && nodeURI[0] != "" {
		u, parseErr := url.Parse(nodeURI[0])
		if parseErr != nil {
			return nil, &GenerateError{Err: parseErr}
		}
		template.URIs = append(template.URIs, u)
	}

	return template, nil
}

// GenerateNodeCert creates an ECDSA P-256 leaf certificate signed by the
// given CA. The leaf includes SANs for all local hostnames and interface IPs.
// ExtKeyUsage includes both ServerAuth and ClientAuth for mTLS.
//
// When nodeURI is provided and non-empty, it is parsed as a URI and appended
// to the leaf's URI SANs so the node carries its SPIFFE identity
// (spiffe://<trust-domain>/node/<raftAddr>). A non-empty URI that fails to
// parse returns a GenerateError so that misconfigured cluster identity is
// caught at certificate generation time rather than silently shipping a cert
// without the SAN.
func GenerateNodeCert(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, validity time.Duration, nodeURI ...string) (certPEM, keyPEM []byte, err error) {
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, &GenerateError{Err: err}
	}

	leafTemplate, err := NodeLeafTemplate(validity, nodeURI...)
	if err != nil {
		return nil, nil, err
	}

	leafCertDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, &GenerateError{Err: err}
	}

	leafKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return nil, nil, &GenerateError{Err: err}
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafCertDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: leafKeyDER})

	return certPEM, keyPEM, nil
}

// IssueLeafForKey signs a node leaf certificate for a caller-supplied public
// key (e.g. a hardware-resident key whose private key never leaves the device)
// using the given CA cert+key PEM. Unlike GenerateNodeCert, the private key is
// NOT generated here — the caller owns it and its crypto.Signer. The leaf
// carries the same template as GenerateNodeCert: CN "qrdb-node", SANs from
// LocalSANs(), KeyUsage DigitalSignature|KeyEncipherment, ExtKeyUsage
// ServerAuth+ClientAuth, NodeValidity lifetime. nodeURI is an optional SPIFFE
// node URI SAN; a non-empty URI that fails to parse returns a GenerateError.
func IssueLeafForKey(caCertPEM, caKeyPEM []byte, pub crypto.PublicKey, nodeURI ...string) (certPEM []byte, err error) {
	caBlock, _ := pem.Decode(caCertPEM)
	if caBlock == nil || caBlock.Type != "CERTIFICATE" {
		return nil, &KeyError{Kind: "CA certificate", Err: errors.New("no CERTIFICATE block found in PEM data")}
	}
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		return nil, &KeyError{Kind: "CA certificate", Err: err}
	}

	keyBlock, _ := pem.Decode(caKeyPEM)
	if keyBlock == nil || keyBlock.Type != "EC PRIVATE KEY" {
		return nil, &KeyError{Kind: "CA private key", Err: errors.New("no EC PRIVATE KEY block found in PEM data")}
	}
	caKey, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, &KeyError{Kind: "CA private key", Err: err}
	}

	leafTemplate, err := NodeLeafTemplate(NodeValidity, nodeURI...)
	if err != nil {
		return nil, err
	}

	leafCertDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, pub, caKey)
	if err != nil {
		return nil, &GenerateError{Err: err}
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafCertDER}), nil
}

// GenerateClientCert creates an ECDSA P-256 client certificate signed by the
// given CA. CN is set to cn and OU to ou. The certificate carries only
// ExtKeyUsageClientAuth so it cannot be used for server authentication.
func GenerateClientCert(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, cn, ou string, validity time.Duration) (certPEM, keyPEM []byte, err error) {
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, &GenerateError{Err: err}
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, &GenerateError{Err: err}
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
		return nil, nil, &GenerateError{Err: err}
	}

	clientKeyDER, err := x509.MarshalECPrivateKey(clientKey)
	if err != nil {
		return nil, nil, &GenerateError{Err: err}
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientCertDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: clientKeyDER})

	return certPEM, keyPEM, nil
}

// GenerateCABundle creates an ephemeral ECDSA P-256 CA and a leaf certificate
// signed by that CA. It returns only the raw PEM material; callers assemble
// tls.Config and x509.CertPool as needed. nodeURI is an optional SPIFFE node
// URI SAN (see GenerateNodeCert).
func GenerateCABundle(nodeURI ...string) (*Material, error) {
	caCertPEM, caKeyPEM, caCert, caKey, err := GenerateCA(CAValidity)
	if err != nil {
		return nil, err
	}

	certPEM, keyPEM, err := GenerateNodeCert(caCert, caKey, NodeValidity, nodeURI...)
	if err != nil {
		return nil, err
	}

	return &Material{
		CACertPEM: caCertPEM,
		CAKeyPEM:  caKeyPEM,
		CertPEM:   certPEM,
		KeyPEM:    keyPEM,
	}, nil
}

// CertificatesOnly returns the CERTIFICATE blocks of pemData, re-encoded, and
// nothing else. Every other block -- private keys above all -- and any bytes
// between blocks are dropped. It returns nil when pemData holds no
// certificate.
//
// It is the filter every path that hands CA material to another node applies
// to its bytes. The cluster CA private key never leaves the node that holds
// it, and a caller that concatenates the key onto the certificate by mistake
// (the bundle shape earlier releases served) must not be able to undo that.
func CertificatesOnly(pemData []byte) []byte {
	var out []byte
	rest := pemData
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return out
		}
		if block.Type == "CERTIFICATE" {
			out = append(out, pem.EncodeToMemory(&pem.Block{Type: block.Type, Bytes: block.Bytes})...)
		}
	}
}

// LoadFromFiles reads TLS certificate material from the given explicit file
// paths. It returns a *Material with CACertPEM, CertPEM, and KeyPEM populated.
// CAKeyPEM is not populated because the CA private key lives at a separate path
// not known to this function. A *LoadError is returned for any file-read
// failure.
func LoadFromFiles(caFile, certFile, keyFile string) (*Material, error) {
	caCertPEM, err := os.ReadFile(caFile)
	if err != nil {
		return nil, &LoadError{Path: caFile, Err: err}
	}
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, &LoadError{Path: certFile, Err: err}
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, &LoadError{Path: keyFile, Err: err}
	}
	return &Material{
		CACertPEM: caCertPEM,
		CertPEM:   certPEM,
		KeyPEM:    keyPEM,
	}, nil
}

// LoadFromDir attempts to load TLS certificate material from the standard file
// names in dataDir: ca.pem, node-cert.pem, node-key.pem. When all three
// required files are absent, (nil, nil) is returned to signal no cached
// material. When some but not all are present — an interrupted write or a
// corrupted directory — a *PartialMaterialError is returned rather than
// silently treating it as "no cache" (which would regenerate a fresh CA and
// fork cluster trust). The optional ca-key.pem is loaded when present and
// populates CAKeyPEM.
func LoadFromDir(dataDir string) (*Material, error) {
	caPath := filepath.Join(dataDir, "ca.pem")
	certPath := filepath.Join(dataDir, "node-cert.pem")
	keyPath := filepath.Join(dataDir, "node-key.pem")

	caOK, certOK, keyOK := fileExists(caPath), fileExists(certPath), fileExists(keyPath)
	if !caOK && !certOK && !keyOK {
		return nil, nil
	}
	if !caOK || !certOK || !keyOK {
		missing := make([]string, 0, 3)
		for path, ok := range map[string]bool{caPath: caOK, certPath: certOK, keyPath: keyOK} {
			if !ok {
				missing = append(missing, path)
			}
		}
		sort.Strings(missing)
		return nil, &PartialMaterialError{Dir: dataDir, Missing: missing}
	}

	caCertPEM, err := os.ReadFile(caPath)
	if err != nil {
		return nil, &LoadError{Path: caPath, Err: err}
	}

	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, &LoadError{Path: certPath, Err: err}
	}

	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, &LoadError{Path: keyPath, Err: err}
	}

	m := &Material{
		CACertPEM: caCertPEM,
		CertPEM:   certPEM,
		KeyPEM:    keyPEM,
	}

	caKeyPath := filepath.Join(dataDir, "ca-key.pem")
	if fileExists(caKeyPath) {
		caKeyPEM, err := os.ReadFile(caKeyPath)
		if err != nil {
			return nil, &LoadError{Path: caKeyPath, Err: err}
		}
		m.CAKeyPEM = caKeyPEM
	}

	return m, nil
}

// LoadCAFromDir loads ca.pem and ca-key.pem from dataDir. Returns
// (nil, nil, nil) when either file is absent, signalling no cached CA.
// Unlike LoadFromDir, this function does not require node-cert.pem or
// node-key.pem — it is used by consumers that issue their own leaf certificate
// (e.g. via IssueLeafForKey) and only need the cluster CA material cached.
func LoadCAFromDir(dataDir string) (caCertPEM, caKeyPEM []byte, err error) {
	caPath := filepath.Join(dataDir, "ca.pem")
	caKeyPath := filepath.Join(dataDir, "ca-key.pem")

	if !fileExists(caPath) || !fileExists(caKeyPath) {
		return nil, nil, nil
	}

	caCertPEM, err = os.ReadFile(caPath)
	if err != nil {
		return nil, nil, &LoadError{Path: caPath, Err: err}
	}

	caKeyPEM, err = os.ReadFile(caKeyPath)
	if err != nil {
		return nil, nil, &LoadError{Path: caKeyPath, Err: err}
	}

	return caCertPEM, caKeyPEM, nil
}

// PersistMaterial writes the TLS material to standard file names in dataDir:
// ca.pem, node-cert.pem, node-key.pem, and optionally ca-key.pem when
// CAKeyPEM is non-empty. The directory is created with 0o700 permissions and
// files with 0o600. Each file is written atomically (write to a temporary file
// in the same directory, then rename) so a crash mid-write never leaves a
// half-written, corrupt PEM file in place.
//
// ca.pem is written last, after every key: its presence means the set it
// belongs to, including the CA key, is already durable. A crash or a failed
// write therefore never leaves a CA certificate whose key was lost, which
// LoadFromDir would accept as a CA that silently cannot sign.
func PersistMaterial(m *Material, dataDir string) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return &WriteError{Path: dataDir, Err: err}
	}

	type file struct {
		name string
		data []byte
	}
	var files []file
	if len(m.CAKeyPEM) > 0 {
		files = append(files, file{"ca-key.pem", m.CAKeyPEM})
	}
	files = append(files,
		file{"node-key.pem", m.KeyPEM},
		file{"node-cert.pem", m.CertPEM},
		file{"ca.pem", m.CACertPEM},
	)

	for _, f := range files {
		path := filepath.Join(dataDir, f.name)
		if err := securefile.WriteFileAtomic(path, f.data, 0o600); err != nil {
			return &WriteError{Path: path, Err: err}
		}
	}

	return nil
}

// PersistCA writes only the CA certificate (ca.pem) and CA private key
// (ca-key.pem) to dataDir. The directory is created with 0o700 permissions
// and files with 0o600, each written atomically. Used when the node leaf
// certificate is issued separately via IssueLeafForKey so there is no
// node-cert.pem or node-key.pem to persist. The key is written first, for the
// reason PersistMaterial gives.
func PersistCA(caCertPEM, caKeyPEM []byte, dataDir string) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return &WriteError{Path: dataDir, Err: err}
	}

	for _, f := range []struct {
		name string
		data []byte
	}{
		{"ca-key.pem", caKeyPEM},
		{"ca.pem", caCertPEM},
	} {
		path := filepath.Join(dataDir, f.name)
		if err := securefile.WriteFileAtomic(path, f.data, 0o600); err != nil {
			return &WriteError{Path: path, Err: err}
		}
	}

	return nil
}

// fileExists returns true when path refers to an existing regular file.
func fileExists(path string) bool {
	// ANY existing entry counts — a directory (or other non-regular file)
	// squatting at a material path must NOT read as "absent": absent means
	// "no cache, generate", and generating a fresh CA because something
	// unreadable occupies ca.pem silently forks cluster trust. Reporting it
	// as present routes the load into os.ReadFile, whose failure (EISDIR,
	// EACCES, ...) fails closed as a *LoadError naming the path.
	_, err := os.Stat(path)
	return err == nil
}

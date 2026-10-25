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

// Package qrdbdharness spins up a real qrdbd process and hands callers
// everything they need to talk to it over mTLS from a test.
//
// It exists because two separate test trees — the root module's
// test/integration suite and the sdk/go/gorm/conformance module — each need
// the identical four primitives:
//
//   - an ephemeral certificate authority that mints the node certificate
//     qrdbd presents and the client certificates callers authenticate with
//     (qrdb's mTLS authenticator maps CN to user and OU to tenant, so the
//     subject is caller-specifiable),
//   - listen ports reserved from a window BELOW the kernel's ephemeral range
//     and bind-verified before use (see internal/netport's window.go for why
//     this is not net.Listen(":0")),
//   - a compiled qrdbd binary plus subprocess start/stop, and
//   - readiness waits for the node host, the admin /health endpoint, and
//     Raft leadership.
//
// Nothing here takes a *testing.T: the GORM conformance module drives it
// from a plain runner binary and needs real errors rather than t.Fatalf. Callers that want fatal-on-error
// ergonomics wrap these calls in their own one-line test helpers.
package qrdbdharness

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
	"github.com/jeremyhahn/go-qrdb/pkg/config"
)

const (
	// DefaultOrganization is the X.509 Organization put on the CA and on every
	// leaf certificate unless overridden.
	DefaultOrganization = "go-qrdb-harness"

	// DefaultCACommonName is the CA's subject common name unless overridden.
	DefaultCACommonName = "qrdbd-harness-ca"

	// DefaultValidity is how long the CA and the certificates it issues remain
	// valid. Harness certificates outlive the process that created them by
	// design: a slow integration run must not start failing handshakes midway.
	DefaultValidity = 2 * time.Hour

	// backdate is how far before "now" certificates become valid, absorbing
	// small clock differences between the test process and the daemon.
	backdate = time.Minute

	// dirMode is the permission mode for directories the harness creates.
	dirMode os.FileMode = 0o700

	// fileMode is the permission mode for the key and certificate files the
	// harness writes.
	fileMode os.FileMode = 0o600

	// caFileName is the file name the CA certificate is written under.
	caFileName = "ca.pem"
)

// CA is an ephemeral ECDSA P-256 certificate authority that signs the
// certificates a harnessed qrdbd node and its clients present to each other.
// Its fields are read-only after NewCA returns.
type CA struct {
	// CertPEM is the PEM-encoded CA certificate.
	CertPEM []byte

	// CertPath is where CertPEM was written, suitable for qrdbd's
	// --tls-ca-file flag and the SDK's mTLS options.
	CertPath string

	// Pool contains only this CA, for verifying the node certificate.
	Pool *x509.CertPool

	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

// caConfig holds the resolved CAOption values.
type caConfig struct {
	organization string
	commonName   string
	validity     time.Duration
}

// CAOption customizes the certificate authority created by NewCA.
type CAOption func(*caConfig)

// WithCAOrganization sets the CA subject's Organization. Empty values are
// ignored so callers can pass through an unset config field.
func WithCAOrganization(organization string) CAOption {
	return func(c *caConfig) {
		if organization != "" {
			c.organization = organization
		}
	}
}

// WithCACommonName sets the CA subject's CommonName.
func WithCACommonName(commonName string) CAOption {
	return func(c *caConfig) {
		if commonName != "" {
			c.commonName = commonName
		}
	}
}

// WithCAValidity sets how long the CA certificate remains valid.
func WithCAValidity(validity time.Duration) CAOption {
	return func(c *caConfig) {
		if validity > 0 {
			c.validity = validity
		}
	}
}

// NewCA creates an ephemeral CA and writes its certificate to dir/ca.pem.
// The caller owns dir and therefore the certificate's lifetime: pass
// t.TempDir() from a test that finishes with the daemon, or os.MkdirTemp from
// a TestMain whose server outlives every individual test.
func NewCA(dir string, opts ...CAOption) (*CA, error) {
	cfg := caConfig{
		organization: DefaultOrganization,
		commonName:   DefaultCACommonName,
		validity:     DefaultValidity,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, &CAError{Op: "generate key", Err: err}
	}

	serial, err := newSerial()
	if err != nil {
		return nil, &CAError{Op: "serial", Err: err}
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{cfg.organization},
			CommonName:   cfg.commonName,
		},
		NotBefore:             now.Add(-backdate),
		NotAfter:              now.Add(cfg.validity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, &CAError{Op: "create cert", Err: err}
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, &CAError{Op: "parse cert", Err: err}
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, &CAError{Op: "mkdir", Err: err}
	}
	certPath := filepath.Join(dir, caFileName)
	if err := os.WriteFile(certPath, certPEM, fileMode); err != nil {
		return nil, &CAError{Op: "write cert", Err: err}
	}

	pool := x509.NewCertPool()
	pool.AddCert(cert)

	return &CA{
		CertPEM:  certPEM,
		CertPath: certPath,
		Pool:     pool,
		cert:     cert,
		key:      key,
	}, nil
}

// LoadCA adopts a certificate authority that already exists on disk — the one
// qrdbd self-generates into its data directory when no TLS material is
// supplied — so tests can issue client certificates the running daemon
// already trusts instead of minting a second, unrelated CA.
func LoadCA(certPath, keyPath string) (*CA, error) {
	// #nosec G304 -- both paths are the test's own: LoadCA exists to adopt the
	// CA qrdbd wrote into a data directory the harness created. No request
	// value reaches either argument.
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, &CAError{Op: "read cert", Err: err}
	}
	// #nosec G304 -- see the note on certPath above.
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, &CAError{Op: "read key", Err: err}
	}

	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, &CAError{Op: "decode cert", Err: errNoPEMBlock}
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, &CAError{Op: "parse cert", Err: err}
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, &CAError{Op: "decode key", Err: errNoPEMBlock}
	}
	key, err := parseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, &CAError{Op: "parse key", Err: err}
	}

	pool := x509.NewCertPool()
	pool.AddCert(cert)

	return &CA{
		CertPEM:  certPEM,
		CertPath: certPath,
		Pool:     pool,
		cert:     cert,
		key:      key,
	}, nil
}

// errNoPEMBlock reports PEM input that contains no block at all.
var errNoPEMBlock = errors.New("no PEM block")

// errNotECDSAKey reports a PKCS#8 key that is not an ECDSA key.
var errNotECDSAKey = errors.New("private key is not ECDSA")

// parseECPrivateKey accepts either the SEC 1 or the PKCS#8 encoding, since
// which one a CA key file uses depends on who wrote it.
func parseECPrivateKey(der []byte) (*ecdsa.PrivateKey, error) {
	if key, err := x509.ParseECPrivateKey(der); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errNotECDSAKey
	}
	return key, nil
}

// KeyPair is a leaf certificate and its private key, both on disk and in
// memory. The paths feed qrdbd's --tls-cert-file/--tls-key-file flags and the
// SDK's mTLS transport options.
type KeyPair struct {
	CertPath string
	KeyPath  string
	CertPEM  []byte
	KeyPEM   []byte
}

// TLSCertificate parses the pair into a tls.Certificate for use in a
// tls.Config.
func (kp *KeyPair) TLSCertificate() (tls.Certificate, error) {
	cert, err := tls.X509KeyPair(kp.CertPEM, kp.KeyPEM)
	if err != nil {
		return tls.Certificate{}, &CertError{CommonName: kp.CertPath, Op: "load key pair", Err: err}
	}
	return cert, nil
}

// certConfig holds the resolved CertOption values.
type certConfig struct {
	organization       string
	commonName         string
	organizationalUnit string
	validity           time.Duration
	serverAuth         bool
	spiffeTrustDomain  string
	spiffePath         string
	uriSAN             string
	clusterTrustDomain string
}

// CertOption customizes a leaf certificate issued by CA.Issue.
type CertOption func(*certConfig)

// WithSubject sets the leaf certificate's CommonName and OrganizationalUnit.
// qrdb's mTLS authenticator maps CN to the user name and OU to the tenant, so
// this is the option that decides which identity a client authenticates as —
// CN=admin, OU=system is the identity firstboot provisions with platform-wide
// authority, while anything else is an ordinary tenant-scoped identity.
// An empty organizationalUnit omits the attribute entirely. A client
// certificate with an OU also names its tenant in the agreeing SPIFFE ID SAN
// spiffe://<OU>/user/<CN>, under the tenant's own trust domain, which the
// authenticator requires (auth.TenantFromClientCert); see
// WithClusterTrustDomain.
func WithSubject(commonName, organizationalUnit string) CertOption {
	return func(c *certConfig) {
		c.commonName = commonName
		c.organizationalUnit = organizationalUnit
	}
}

// WithCertOrganization sets the leaf certificate subject's Organization.
func WithCertOrganization(organization string) CertOption {
	return func(c *certConfig) {
		if organization != "" {
			c.organization = organization
		}
	}
}

// WithCertValidity sets how long the leaf certificate remains valid.
func WithCertValidity(validity time.Duration) CertOption {
	return func(c *certConfig) {
		if validity > 0 {
			c.validity = validity
		}
	}
}

// WithSPIFFEID adds a SPIFFE URI SAN of the form spiffe://<trustDomain>/<path>.
// qrdb's IsolationRaft cluster key delivery refuses to hand a tenant key to a
// recipient whose certificate carries no SPIFFE SAN, so node certificates used
// in those flows must have one.
func WithSPIFFEID(trustDomain, path string) CertOption {
	return func(c *certConfig) {
		c.spiffeTrustDomain = trustDomain
		c.spiffePath = path
	}
}

// WithURISAN makes uri, verbatim, the certificate's only URI SAN, replacing
// the SPIFFE ID WithSPIFFEID or the subject OU would give it. It is how a test
// presents a URI SAN that is not a SPIFFE ID at all, which no production
// issuer mints and the daemon refuses.
func WithURISAN(uri string) CertOption {
	return func(c *certConfig) { c.uriSAN = uri }
}

// WithClusterTrustDomain sets the cluster trust domain the derived tenant
// SPIFFE ID SAN is checked against: the daemon's auth.cluster_trust_domain,
// which no tenant may be.
// Unset, it is config.DefaultClusterTrustDomain, the domain of a daemon that
// configures neither. WithSPIFFEID replaces the derived SAN outright, which is
// how a test presents a certificate whose SAN and OU disagree.
func WithClusterTrustDomain(trustDomain string) CertOption {
	return func(c *certConfig) {
		if trustDomain != "" {
			c.clusterTrustDomain = trustDomain
		}
	}
}

// WithServerAuth marks the certificate as a node (server) certificate: it adds
// the ServerAuth extended key usage on top of ClientAuth and SANs for
// localhost, this host's name, the loopback and unspecified addresses, and
// every non-loopback interface address, so the same certificate works whether
// the daemon is reached over 127.0.0.1, a container IP, or a host name.
func WithServerAuth() CertOption {
	return func(c *certConfig) { c.serverAuth = true }
}

// Issue signs a leaf certificate and writes it to dir/<CN>-cert.pem and
// dir/<CN>-key.pem. Two certificates with the same common name issued into the
// same directory overwrite each other; give them separate directories or
// distinct subjects.
func (ca *CA) Issue(dir string, opts ...CertOption) (*KeyPair, error) {
	cfg := certConfig{
		organization:       DefaultOrganization,
		validity:           DefaultValidity,
		clusterTrustDomain: config.DefaultClusterTrustDomain,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	if err := validateCommonName(cfg.commonName); err != nil {
		return nil, err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, &CertError{CommonName: cfg.commonName, Op: "generate key", Err: err}
	}

	serial, err := newSerial()
	if err != nil {
		return nil, &CertError{CommonName: cfg.commonName, Op: "serial", Err: err}
	}

	subject := pkix.Name{
		Organization: []string{cfg.organization},
		CommonName:   cfg.commonName,
	}
	if cfg.organizationalUnit != "" {
		subject.OrganizationalUnit = []string{cfg.organizationalUnit}
	}

	extKeyUsage := []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	var dnsNames []string
	var ips []net.IP
	if cfg.serverAuth {
		extKeyUsage = append(extKeyUsage, x509.ExtKeyUsageServerAuth)
		// There is exactly ONE local-SAN implementation in this repository, and
		// it is the production one. The harness carried a copy for a while; the
		// copy drifted (it kept 0.0.0.0 and :: after clustertls dropped them),
		// and the drift let a "len(ips) >= 4" assertion be copied from this
		// package into cmd/qrdbd, where it was wrong and reddened every
		// container gate for three weeks (#297, #304).
		//
		// Resist adding wildcards back "so any bind address validates". A
		// wildcard is a BIND address, and TLS verification never sees one: the
		// client verifies the name it DIALED. Every caller of this harness was
		// enumerated for #304 and every one of them dials 127.0.0.1 or
		// localhost, both of which clustertls.LocalSANs already covers. The
		// tests bind qrdbd to 0.0.0.0 and still connect to 127.0.0.1.
		dnsNames, ips = clustertls.LocalSANs()
	}

	var uris []*url.URL
	switch {
	case cfg.uriSAN != "":
		uri, err := url.Parse(cfg.uriSAN)
		if err != nil {
			return nil, &CertError{CommonName: cfg.commonName, Op: "parse uri san " + cfg.uriSAN, Err: err}
		}
		uris = append(uris, uri)
	case cfg.spiffeTrustDomain != "":
		spiffeID := "spiffe://" + cfg.spiffeTrustDomain + "/" + strings.TrimPrefix(cfg.spiffePath, "/")
		uri, err := url.Parse(spiffeID)
		if err != nil {
			return nil, &CertError{CommonName: cfg.commonName, Op: "parse spiffe id " + spiffeID, Err: err}
		}
		uris = append(uris, uri)
	case cfg.organizationalUnit != "" && !cfg.serverAuth:
		// A tenant principal: its SAN agrees with its OU, the way every
		// production issuer mints one. A node leaf keeps its node rule.
		uris, err = auth.TenantPrincipalURIs(cfg.clusterTrustDomain, cfg.organizationalUnit, spiffe.KindUser, cfg.commonName)
		if err != nil {
			return nil, &CertError{CommonName: cfg.commonName, Op: "tenant spiffe id", Err: err}
		}
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               subject,
		DNSNames:              dnsNames,
		IPAddresses:           ips,
		URIs:                  uris,
		NotBefore:             now.Add(-backdate),
		NotAfter:              now.Add(cfg.validity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           extKeyUsage,
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, &CertError{CommonName: cfg.commonName, Op: "create cert", Err: err}
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, &CertError{CommonName: cfg.commonName, Op: "marshal key", Err: err}
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, &CertError{CommonName: cfg.commonName, Op: "mkdir", Err: err}
	}
	certPath := filepath.Join(dir, cfg.commonName+"-cert.pem")
	keyPath := filepath.Join(dir, cfg.commonName+"-key.pem")
	if err := os.WriteFile(certPath, certPEM, fileMode); err != nil {
		return nil, &CertError{CommonName: cfg.commonName, Op: "write cert", Err: err}
	}
	if err := os.WriteFile(keyPath, keyPEM, fileMode); err != nil {
		return nil, &CertError{CommonName: cfg.commonName, Op: "write key", Err: err}
	}

	return &KeyPair{
		CertPath: certPath,
		KeyPath:  keyPath,
		CertPEM:  certPEM,
		KeyPEM:   keyPEM,
	}, nil
}

// TLSFlags returns the qrdbd command-line flags that point the daemon at this
// CA and the given node key pair.
func (ca *CA) TLSFlags(node *KeyPair) []string {
	return []string{
		"--tls-ca-file", ca.CertPath,
		"--tls-cert-file", node.CertPath,
		"--tls-key-file", node.KeyPath,
	}
}

// validateCommonName rejects subjects that cannot be turned into a file name.
func validateCommonName(commonName string) error {
	if commonName == "" {
		return &InvalidSubjectError{CommonName: commonName, Reason: "common name must not be empty"}
	}
	if strings.ContainsRune(commonName, os.PathSeparator) || strings.ContainsRune(commonName, '/') {
		return &InvalidSubjectError{CommonName: commonName, Reason: "common name must not contain a path separator"}
	}
	if commonName == "." || commonName == ".." {
		return &InvalidSubjectError{CommonName: commonName, Reason: "common name must not be a path element"}
	}
	return nil
}

// newSerial draws a random 128-bit certificate serial number.
func newSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

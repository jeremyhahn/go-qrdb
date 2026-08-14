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
// It exists because three separate test trees — the root module's
// test/integration suite, the cmd/qrdb-sqlite gateway's integration module,
// and the sdk/go/gorm/conformance module — each need the identical four
// primitives:
//
//   - an ephemeral certificate authority that mints the node certificate
//     qrdbd presents and the client certificates callers authenticate with
//     (qrdb's mTLS authenticator maps CN to user and OU to tenant, so the
//     subject is caller-specifiable),
//   - listen ports reserved from a window BELOW the kernel's ephemeral range
//     and bind-verified before use (see ports.go for why this is not
//     net.Listen(":0")),
//   - a compiled qrdbd binary plus subprocess start/stop, and
//   - readiness waits for the node host, the admin /health endpoint, and
//     Raft leadership.
//
// Nothing here takes a *testing.T: the sqlite gateway drives it from TestMain
// and the GORM conformance module drives it from a plain runner binary, and
// both need real errors rather than t.Fatalf. Callers that want fatal-on-error
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
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, &CAError{Op: "read cert", Err: err}
	}
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
}

// CertOption customizes a leaf certificate issued by CA.Issue.
type CertOption func(*certConfig)

// WithSubject sets the leaf certificate's CommonName and OrganizationalUnit.
// qrdb's mTLS authenticator maps CN to the user name and OU to the tenant, so
// this is the option that decides which identity a client authenticates as —
// CN=admin, OU=system is the identity firstboot provisions with platform-wide
// authority, while anything else is an ordinary tenant-scoped identity.
// An empty organizationalUnit omits the attribute entirely.
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
		organization: DefaultOrganization,
		validity:     DefaultValidity,
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
		dnsNames, ips = localSANs()
	}

	var uris []*url.URL
	if cfg.spiffeTrustDomain != "" {
		spiffeID := "spiffe://" + cfg.spiffeTrustDomain + "/" + strings.TrimPrefix(cfg.spiffePath, "/")
		uri, err := url.Parse(spiffeID)
		if err != nil {
			return nil, &CertError{CommonName: cfg.commonName, Op: "parse spiffe id " + spiffeID, Err: err}
		}
		uris = append(uris, uri)
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

// localSANs returns the DNS names and IP addresses a node certificate must
// cover to be reachable however a test addresses the daemon.
func localSANs() ([]string, []net.IP) {
	dnsNames := []string{"localhost"}
	ips := []net.IP{
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

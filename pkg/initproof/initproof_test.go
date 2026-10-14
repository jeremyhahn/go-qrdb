// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package initproof

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
)

// tlsPair completes a real in-memory TLS handshake at version and returns
// both ends' connection states.
func tlsPair(t *testing.T, version uint16) (client, server tls.ConnectionState) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "initproof.test"},
		DNSNames:     []string{"initproof.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)

	cConn, sConn := net.Pipe()
	t.Cleanup(func() { _ = cConn.Close(); _ = sConn.Close() })
	srv := tls.Server(sConn, &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   version,
		MaxVersion:   version,
	})
	cli := tls.Client(cConn, &tls.Config{
		RootCAs:    roots,
		ServerName: "initproof.test",
		MinVersion: version,
		MaxVersion: version,
	})
	errc := make(chan error, 1)
	go func() { errc <- srv.Handshake() }()
	if err := cli.Handshake(); err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	if err := <-errc; err != nil {
		t.Fatalf("server handshake: %v", err)
	}
	return cli.ConnectionState(), srv.ConnectionState()
}

func TestExporter_BothEndsDeriveTheSameBinding(t *testing.T) {
	c, s := tlsPair(t, tls.VersionTLS13)
	ce, err := Exporter(&c)
	if err != nil {
		t.Fatalf("client exporter: %v", err)
	}
	se, err := Exporter(&s)
	if err != nil {
		t.Fatalf("server exporter: %v", err)
	}
	if len(ce) != ExporterLength || !bytes.Equal(ce, se) {
		t.Fatalf("exporters differ or wrong length: %d %x %x", len(ce), ce, se)
	}
}

func TestExporter_DiffersPerConnection(t *testing.T) {
	c1, _ := tlsPair(t, tls.VersionTLS13)
	c2, _ := tlsPair(t, tls.VersionTLS13)
	e1, err := Exporter(&c1)
	if err != nil {
		t.Fatal(err)
	}
	e2, err := Exporter(&c2)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(e1, e2) {
		t.Fatal("two connections derived the same exporter")
	}
}

func TestExporter_RefusesStatesWithoutTLS13KeyingMaterial(t *testing.T) {
	c12, _ := tlsPair(t, tls.VersionTLS12)
	cases := map[string]*tls.ConnectionState{
		"nil":       nil,
		"tls12":     &c12,
		"handshake": {Version: tls.VersionTLS13},
		// An assembled state, like the embedded transport's forwarded
		// caller, claims TLS 1.3 but has no keying material.
		"synthetic": {Version: tls.VersionTLS13, HandshakeComplete: true},
	}
	for name, cs := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := Exporter(cs)
			var ne *NoExporterError
			if !errors.As(err, &ne) || got != nil {
				t.Fatalf("Exporter = %x, %v; want *NoExporterError", got, err)
			}
		})
	}
}

func TestMessage_FieldsAreUnambiguous(t *testing.T) {
	a := Message("ab", []byte("c"), []byte("x"))
	b := Message("a", []byte("bc"), []byte("x"))
	if bytes.Equal(a, b) {
		t.Fatal("tenant/nonce boundary is ambiguous")
	}
	if !bytes.HasPrefix(a, []byte(messageContext+"\x00ab\x00")) {
		t.Fatalf("message lacks domain separation: %q", a)
	}
}

func signers(t *testing.T) map[string]crypto.Signer {
	t.Helper()
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, ed, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]crypto.Signer{"ecdsa": ec, "ed25519": ed, "rsa": rs}
}

func TestSignVerify_RoundTripsEveryAcceptedKeyType(t *testing.T) {
	nonce, exporter := []byte("nonce-0123456789"), bytes.Repeat([]byte{7}, ExporterLength)
	for name, s := range signers(t) {
		t.Run(name, func(t *testing.T) {
			sig, err := Sign(s, "acme", nonce, exporter)
			if err != nil {
				t.Fatalf("Sign: %v", err)
			}
			if !Verify(s.Public(), "acme", nonce, exporter, sig) {
				t.Fatal("own signature does not verify")
			}
		})
	}
}

func TestVerify_RefusesEveryOtherBinding(t *testing.T) {
	s := signers(t)
	key, other := s["ecdsa"], s["ed25519"]
	nonce, exporter := []byte("nonce-0123456789"), bytes.Repeat([]byte{7}, ExporterLength)
	sig, err := Sign(key, "acme", nonce, exporter)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func() bool{
		"wrong key":         func() bool { return Verify(other.Public(), "acme", nonce, exporter, sig) },
		"other tenant":      func() bool { return Verify(key.Public(), "globex", nonce, exporter, sig) },
		"other nonce":       func() bool { return Verify(key.Public(), "acme", []byte("nonce-other"), exporter, sig) },
		"other connection":  func() bool { return Verify(key.Public(), "acme", nonce, bytes.Repeat([]byte{8}, ExporterLength), sig) },
		"empty nonce":       func() bool { return Verify(key.Public(), "acme", nil, exporter, sig) },
		"empty exporter":    func() bool { return Verify(key.Public(), "acme", nonce, nil, sig) },
		"empty signature":   func() bool { return Verify(key.Public(), "acme", nonce, exporter, nil) },
		"unsupported key":   func() bool { return Verify("not a key", "acme", nonce, exporter, sig) },
		"corrupt signature": func() bool { return Verify(key.Public(), "acme", nonce, exporter, append([]byte{0}, sig...)) },
	}
	for name, verify := range cases {
		t.Run(name, func(t *testing.T) {
			if verify() {
				t.Fatal("verified")
			}
		})
	}
}

// replayExporter proves the property the protocol exists for: a proof signed
// for one live connection does not verify against another's exporter.
func TestVerify_ReplayOnAnotherRealConnectionFails(t *testing.T) {
	key := signers(t)["ecdsa"]
	c1, s1 := tlsPair(t, tls.VersionTLS13)
	_, s2 := tlsPair(t, tls.VersionTLS13)
	e1, err := Exporter(&c1)
	if err != nil {
		t.Fatal(err)
	}
	nonce := []byte("nonce-0123456789")
	sig, err := Sign(key, "acme", nonce, e1)
	if err != nil {
		t.Fatal(err)
	}
	se1, err := Exporter(&s1)
	if err != nil {
		t.Fatal(err)
	}
	se2, err := Exporter(&s2)
	if err != nil {
		t.Fatal(err)
	}
	if !Verify(key.Public(), "acme", nonce, se1, sig) {
		t.Fatal("proof does not verify on its own connection")
	}
	if Verify(key.Public(), "acme", nonce, se2, sig) {
		t.Fatal("proof verified on another connection")
	}
}

// opaqueSigner is a crypto.Signer whose key type the verifier does not
// accept.
type opaqueSigner struct{}

func (opaqueSigner) Public() crypto.PublicKey                                  { return "opaque" }
func (opaqueSigner) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) { return nil, nil }

func TestSign_RefusesWhatCannotBeVerified(t *testing.T) {
	key := signers(t)["ecdsa"]
	exporter := bytes.Repeat([]byte{7}, ExporterLength)
	cases := map[string]func() ([]byte, error){
		"nil signer":     func() ([]byte, error) { return Sign(nil, "acme", []byte("n"), exporter) },
		"opaque key":     func() ([]byte, error) { return Sign(opaqueSigner{}, "acme", []byte("n"), exporter) },
		"empty nonce":    func() ([]byte, error) { return Sign(key, "acme", nil, exporter) },
		"empty exporter": func() ([]byte, error) { return Sign(key, "acme", []byte("n"), nil) },
	}
	for name, sign := range cases {
		t.Run(name, func(t *testing.T) {
			sig, err := sign()
			var us *mtls.UnsupportedSignerError
			if !errors.As(err, &us) || sig != nil {
				t.Fatalf("Sign = %x, %v; want *mtls.UnsupportedSignerError", sig, err)
			}
		})
	}
}

func TestErrors_TextAndUnwrap(t *testing.T) {
	cause := errors.New("renegotiation enabled")
	ne := &NoExporterError{Err: cause}
	if !errors.Is(ne, cause) || !strings.Contains(ne.Error(), cause.Error()) {
		t.Fatalf("NoExporterError does not carry its cause: %v", ne)
	}
	if (&NoExporterError{}).Unwrap() != nil || !strings.Contains((&NoExporterError{}).Error(), "TLS 1.3") {
		t.Fatal("bare NoExporterError text")
	}
	if got := (&ChallengeError{Reason: ReasonExpired}).Error(); !strings.Contains(got, string(ReasonExpired)) {
		t.Fatalf("ChallengeError text %q", got)
	}
	if got := (&TooManyChallengesError{Limit: 3}).Error(); !strings.Contains(got, "3") {
		t.Fatalf("TooManyChallengesError text %q", got)
	}
}

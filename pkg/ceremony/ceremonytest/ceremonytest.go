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

// Package ceremonytest provides a ceremony CA for tests outside pkg/ceremony.
// It is the one place those tests construct the CA, so a change to how the
// ceremony CA signs changes this package rather than every test that needs
// one. It is imported by tests only; a production import of it is a defect.
package ceremonytest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
)

// NewCA returns a ceremony CA over a fresh self-signed ECDSA P-256 CA
// certificate valid for a day, and that certificate in PEM so a test can
// trust the certificates the CA issues. It fails the test on any error.
func NewCA(t testing.TB, commonName string) (*ceremony.LocalCA, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	check(t, "generate CA key", err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	check(t, "self-sign CA certificate", err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	check(t, "marshal CA key", err)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	ca, err := ceremony.NewLocalCA(certPEM, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	check(t, "NewLocalCA", err)
	return ca, certPEM
}

// check fails t when err is not nil, naming the step that failed.
func check(t testing.TB, step string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("ceremonytest: %s: %v", step, err)
	}
}

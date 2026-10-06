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

package cert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// The fingerprint of a CSR's key is the thumbprint a certificate issued for
// that key is bound by -- the value an invitation pin is compared against.
func TestCSRKeyFingerprint_IsTheIssuedCertificatesThumbprint(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	fp, err := CSRKeyFingerprint(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
	if err != nil {
		t.Fatalf("CSRKeyFingerprint: %v", err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatal(err)
	}
	if fp != user.ComputeSPKIThumbprint(c) || !user.IsValidSPKIThumbprint(fp) {
		t.Fatalf("fingerprint %q is not the certificate's thumbprint %q", fp, user.ComputeSPKIThumbprint(c))
	}
}

func TestCSRKeyFingerprint_RefusesABadCSR(t *testing.T) {
	var bad *InvalidCSRError
	if _, err := CSRKeyFingerprint([]byte("not a csr")); !errors.As(err, &bad) {
		t.Fatalf("err = %v, want *InvalidCSRError", err)
	}
}

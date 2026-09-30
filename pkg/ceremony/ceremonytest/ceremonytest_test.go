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

package ceremonytest

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
)

// TestNewCA_ReturnsTheCAItsPEMNames proves the returned PEM is the CA's own
// certificate, named as asked.
func TestNewCA_ReturnsTheCAItsPEMNames(t *testing.T) {
	ca, certPEM := NewCA(t, "Fixture CA")
	cert, err := ca.CACertificate()
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("the returned certificate is not PEM")
	}
	if string(block.Bytes) != string(cert.Raw) {
		t.Fatal("the returned PEM is not the CA's certificate")
	}
	if cert.Subject.CommonName != "Fixture CA" || !cert.IsCA {
		t.Fatalf("CA certificate = %q (IsCA %v)", cert.Subject.CommonName, cert.IsCA)
	}
}

// TestNewCA_IsFreshAndSelfSigned proves each call makes a new CA whose
// certificate verifies against itself.
func TestNewCA_IsFreshAndSelfSigned(t *testing.T) {
	a, aPEM := NewCA(t, "A")
	_, bPEM := NewCA(t, "B")
	if string(aPEM) == string(bPEM) {
		t.Fatal("two calls returned the same CA")
	}
	cert, err := a.CACertificate()
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(aPEM)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool}); err != nil {
		t.Fatalf("CA certificate does not verify against itself: %v", err)
	}
}

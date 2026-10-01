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

package clustertls_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
)

// TestCertificatesOnly_DropsEveryKeyEncoding feeds a bundle interleaving the
// CA certificate with private keys in each encoding the stack produces and
// asserts only the certificates survive, in order.
func TestCertificatesOnly_DropsEveryKeyEncoding(t *testing.T) {
	caCertPEM, caKeyPEM, _, _, err := clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	other, _, _, _, err := clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	pkcs8PEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})

	var in []byte
	for _, part := range [][]byte{caKeyPEM, caCertPEM, pkcs8PEM, []byte("junk between blocks\n"), other} {
		in = append(in, part...)
	}

	got := clustertls.CertificatesOnly(in)
	if bytes.Contains(got, []byte("PRIVATE KEY")) {
		t.Fatalf("output carries key material:\n%s", got)
	}
	want := append(append([]byte{}, caCertPEM...), other...)
	if !bytes.Equal(got, want) {
		t.Fatalf("output = %q, want the two certificates in input order", got)
	}
}

// TestCertificatesOnly_NoCertificateYieldsNil: input with no certificate --
// empty, not PEM, or key material alone -- yields nil, which callers read as
// "nothing to serve".
func TestCertificatesOnly_NoCertificateYieldsNil(t *testing.T) {
	_, caKeyPEM, _, _, err := clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	for name, in := range map[string][]byte{
		"nil":      nil,
		"not PEM":  []byte("not pem at all"),
		"key only": caKeyPEM,
	} {
		t.Run(name, func(t *testing.T) {
			if got := clustertls.CertificatesOnly(in); got != nil {
				t.Fatalf("CertificatesOnly(%s) = %q, want nil", name, got)
			}
		})
	}
}

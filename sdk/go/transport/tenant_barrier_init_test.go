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

package transport

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/initproof"
	"github.com/jeremyhahn/go-qrdb/pkg/initproof/initprooftest"
)

var testNonce = []byte("0123456789abcdef0123456789abcdef")

// TestProveTenantBarrierInit_VerifiesOnTheServerHalf proves over a real TLS
// 1.3 handshake: the proof built from the CLIENT half verifies against the
// exporter the SERVER half derives, for the same tenant and nonce.
func TestProveTenantBarrierInit_VerifiesOnTheServerHalf(t *testing.T) {
	cert := initprooftest.Certificate(t, "acme-admin")
	conn := initprooftest.Dial(t, cert)
	signer := cert.PrivateKey.(crypto.Signer)

	proof, err := ProveTenantBarrierInit(signer, "acme", testNonce, &conn.Client)
	if err != nil {
		t.Fatalf("ProveTenantBarrierInit: %v", err)
	}
	pub, err := x509.ParsePKIXPublicKey(proof.PublicKey)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	exporter, err := initproof.Exporter(&conn.Server)
	if err != nil {
		t.Fatalf("server exporter: %v", err)
	}
	if !initproof.Verify(pub, "acme", proof.Nonce, exporter, proof.Signature) {
		t.Fatal("the proof does not verify on the server half of the connection")
	}
	if initproof.Verify(pub, "globex", proof.Nonce, exporter, proof.Signature) {
		t.Fatal("the proof verified for another tenant")
	}
}

func TestProveTenantBarrierInit_NoProofAsked(t *testing.T) {
	for _, nonce := range [][]byte{nil, {}} {
		proof, err := ProveTenantBarrierInit(nil, "acme", nonce, nil)
		if err != nil {
			t.Fatalf("ProveTenantBarrierInit(nonce=%v): %v", nonce, err)
		}
		if len(proof.Nonce)+len(proof.PublicKey)+len(proof.Signature) != 0 {
			t.Fatalf("proof = %+v; want empty when the node asked none", proof)
		}
	}
}

func TestProveTenantBarrierInit_SignerRequired(t *testing.T) {
	_, err := ProveTenantBarrierInit(nil, "acme", testNonce, nil)
	var need *InitSignerRequiredError
	if !errors.As(err, &need) {
		t.Fatalf("err = %T (%v); want *InitSignerRequiredError", err, err)
	}
	if need.TenantID != "acme" || !strings.Contains(need.Error(), `"acme"`) {
		t.Fatalf("error does not name the tenant: %v", need)
	}
}

func TestProveTenantBarrierInit_NoExporter(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	_, err = ProveTenantBarrierInit(key, "acme", testNonce, nil)
	var noExp *initproof.NoExporterError
	if !errors.As(err, &noExp) {
		t.Fatalf("err = %T (%v); want *initproof.NoExporterError", err, err)
	}
}

// opaqueSigner is a signer whose public key has no SubjectPublicKeyInfo
// encoding.
type opaqueSigner struct{}

func (opaqueSigner) Public() crypto.PublicKey { return struct{}{} }

func (opaqueSigner) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return nil, errors.New("opaqueSigner: never reached")
}

func TestProveTenantBarrierInit_UnencodablePublicKey(t *testing.T) {
	_, err := ProveTenantBarrierInit(opaqueSigner{}, "acme", testNonce, nil)
	var pkErr *InitPublicKeyError
	if !errors.As(err, &pkErr) {
		t.Fatalf("err = %T (%v); want *InitPublicKeyError", err, err)
	}
	if pkErr.Unwrap() == nil || !strings.Contains(pkErr.Error(), "public key") {
		t.Fatalf("InitPublicKeyError = %v; want the encoding failure wrapped", pkErr)
	}
}

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

package sdk_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"slices"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

func TestSDK_QuorumFactorMTLS(t *testing.T) {
	if sdk.QuorumFactorMTLS != "mtls" {
		t.Fatalf("QuorumFactorMTLS = %q, want %q", sdk.QuorumFactorMTLS, "mtls")
	}
}

func TestSDK_ParseHolderSignerPEM(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	signer, err := sdk.ParseHolderSignerPEM(keyPEM)
	if err != nil {
		t.Fatalf("ParseHolderSignerPEM: %v", err)
	}
	if signer == nil || signer.Public() == nil {
		t.Fatal("expected a signer with a public key")
	}
}

func TestSDK_ParseHolderSignerPEM_Invalid(t *testing.T) {
	if _, err := sdk.ParseHolderSignerPEM([]byte("not pem")); err == nil {
		t.Fatal("expected an error for non-PEM input")
	}
}

func TestSDK_AvailableCodecs(t *testing.T) {
	codecs := sdk.AvailableCodecs()
	if !slices.Contains(codecs, "json") {
		t.Fatalf("AvailableCodecs() = %v, want it to include json", codecs)
	}
}

func TestSDK_EncodePKCS12_InvalidKey(t *testing.T) {
	if _, err := sdk.EncodePKCS12([]byte("cert"), []byte("not pem"), nil, "pw"); err == nil {
		t.Fatal("expected an error for an invalid key PEM")
	}
}

func TestSDK_EncodePKCS12_InvalidCert(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if _, err := sdk.EncodePKCS12([]byte("not pem"), keyPEM, nil, "pw"); err == nil {
		t.Fatal("expected an error for an invalid certificate PEM")
	}
}

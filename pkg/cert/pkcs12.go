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

package cert

import (
	"crypto"
	"crypto/fips140"
	"crypto/x509"
	"encoding/pem"

	"software.sslmate.com/src/go-pkcs12"
)

// EncodePKCS12 encodes a certificate, private key, and optional CA chain
// into a PKCS#12 archive protected by the given passphrase.
//
// This is a LOCAL, no-network encoder, exported for callers that already
// hold the private key on the machine running them -- the CLI's
// "ca export-bundle" and the desktop app's export binding, both of which
// read certPEM/keyPEM from local files or local state. There is no
// server-side equivalent: no node ever receives a private key to encode on
// a caller's behalf (go-qrdb security review).
//
// Under GODEBUG=fips140=only the encode is refused up front: the PKCS#12
// localKeyId attribute is a SHA-1 fingerprint of the certificate, and
// crypto/sha1 panics rather than returning an error under strict enforcement.
// The guard is on fips140.Enforced(), not Enabled(), because that SHA-1 is an
// interoperability identifier rather than a protection mechanism -- nothing in
// FIPS 140-3, FedRAMP, PCI DSS or HIPAA prohibits it, so a deployment running
// GODEBUG=fips140=on keeps the export. See docs/seal/fips140-mode.md.
func EncodePKCS12(certPEM, keyPEM, caCertPEM []byte, passphrase string) ([]byte, error) {
	if fips140.Enforced() {
		return nil, &CertExportError{Err: &PKCS12FIPSUnavailableError{}}
	}

	// Decode private key PEM.
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, &CertExportError{Err: &ValidationError{Field: "keyPEM", Reason: "invalid PEM data"}}
	}
	privKey, err := parsePrivateKeyBlock(keyBlock)
	if err != nil {
		return nil, &CertExportError{Err: err}
	}

	// Decode certificate PEM.
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, &CertExportError{Err: &ValidationError{Field: "certPEM", Reason: "invalid PEM data"}}
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, &CertExportError{Err: err}
	}
	if !publicKeyMatches(privKey, cert.PublicKey) {
		return nil, &CertExportError{Err: &ValidationError{Field: "keyPEM", Reason: "the private key is not the certificate's key"}}
	}

	// Decode CA certificate PEM for the chain.
	var caCerts []*x509.Certificate
	if len(caCertPEM) > 0 {
		caBlock, _ := pem.Decode(caCertPEM)
		if caBlock != nil {
			caCert, err := x509.ParseCertificate(caBlock.Bytes)
			if err != nil {
				return nil, &CertExportError{Err: err}
			}
			caCerts = append(caCerts, caCert)
		}
	}

	// Encode to PKCS#12.
	encoder := pkcs12.Modern2023
	p12Data, err := encoder.Encode(privKey, cert, caCerts, passphrase)
	if err != nil {
		return nil, &CertExportError{Err: err}
	}

	return p12Data, nil
}

// parsePrivateKeyBlock decodes a client private key in any encoding a client
// key is written in: PKCS#8 ("PRIVATE KEY", what qrdb's own CLI writes and
// the only encoding Ed25519 has), SEC 1 ("EC PRIVATE KEY") and PKCS#1 ("RSA
// PRIVATE KEY"). The key types are the ones CheckClientKey admits for issuance.
func parsePrivateKeyBlock(block *pem.Block) (crypto.Signer, error) {
	parse, ok := privateKeyParsers[block.Type]
	if !ok {
		return nil, &ValidationError{Field: "keyPEM", Reason: "unsupported PEM block type " + block.Type}
	}
	key, err := parse(block.Bytes)
	if err != nil {
		return nil, err
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, &ValidationError{Field: "keyPEM", Reason: "the key cannot sign"}
	}
	if err := CheckClientKey(signer.Public()); err != nil {
		return nil, err
	}
	return signer, nil
}

// privateKeyParsers maps a private key's PEM block type to its decoder.
var privateKeyParsers = map[string]func([]byte) (any, error){
	"PRIVATE KEY":     x509.ParsePKCS8PrivateKey,
	"EC PRIVATE KEY":  func(der []byte) (any, error) { return x509.ParseECPrivateKey(der) },
	"RSA PRIVATE KEY": func(der []byte) (any, error) { return x509.ParsePKCS1PrivateKey(der) },
}

// publicKeyMatches reports whether signer's public key is pub.
func publicKeyMatches(signer crypto.Signer, pub crypto.PublicKey) bool {
	eq, ok := signer.Public().(interface{ Equal(crypto.PublicKey) bool })
	return ok && eq.Equal(pub)
}

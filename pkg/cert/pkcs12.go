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
	"crypto/fips140"
	"crypto/x509"
	"encoding/pem"

	"software.sslmate.com/src/go-pkcs12"
)

// encodePKCS12 encodes a certificate, private key, and optional CA chain
// into a PKCS#12 archive protected by the given passphrase.
//
// Under GODEBUG=fips140=only the encode is refused up front: the PKCS#12
// localKeyId attribute is a SHA-1 fingerprint of the certificate, and
// crypto/sha1 panics rather than returning an error under strict enforcement.
// The guard is on fips140.Enforced(), not Enabled(), because that SHA-1 is an
// interoperability identifier rather than a protection mechanism -- nothing in
// FIPS 140-3, FedRAMP, PCI DSS or HIPAA prohibits it, so a deployment running
// GODEBUG=fips140=on keeps the export. See docs/seal/fips140-mode.md.
func encodePKCS12(certPEM, keyPEM, caCertPEM []byte, passphrase string) ([]byte, error) {
	if fips140.Enforced() {
		return nil, &CertExportError{Err: &PKCS12FIPSUnavailableError{}}
	}

	// Decode private key PEM.
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, &CertExportError{Err: &ValidationError{Field: "keyPEM", Reason: "invalid PEM data"}}
	}
	privKey, err := x509.ParseECPrivateKey(keyBlock.Bytes)
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

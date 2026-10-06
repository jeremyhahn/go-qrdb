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
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
)

// minRSABits is the smallest RSA key a client certificate is issued for.
const minRSABits = 2048

// acceptedCurves are the ECDSA curves a client certificate is issued for.
var acceptedCurves = map[elliptic.Curve]bool{
	elliptic.P256(): true,
	elliptic.P384(): true,
	elliptic.P521(): true,
}

// ParseCSR decodes a PEM certificate signing request and returns the public
// key it requests a certificate for, after checking the request's own
// signature -- proof that the requester holds the private key -- and that the
// key is one a client certificate is issued for (ECDSA P-256/384/521, RSA of
// at least 2048 bits, or Ed25519).
//
// Nothing else in the request is honoured: the certificate's identity -- its
// Subject and SPIFFE SAN -- is the account the issuer names, never what the
// requester wrote.
func ParseCSR(csrPEM []byte) (crypto.PublicKey, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, &InvalidCSRError{Reason: "it is not a PEM CERTIFICATE REQUEST"}
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, &InvalidCSRError{Reason: "it does not parse", Err: err}
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, &InvalidCSRError{Reason: "its signature does not verify", Err: err}
	}
	if err := CheckClientKey(csr.PublicKey); err != nil {
		return nil, err
	}
	return csr.PublicKey, nil
}

// CheckClientKey reports whether pub is a key a client certificate is issued
// for, as a *InvalidCSRError when it is not.
func CheckClientKey(pub crypto.PublicKey) error {
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		if !acceptedCurves[k.Curve] {
			return &InvalidCSRError{Reason: "its ECDSA curve is not P-256, P-384 or P-521"}
		}
	case *rsa.PublicKey:
		if k.N.BitLen() < minRSABits {
			return &InvalidCSRError{Reason: "its RSA key is shorter than 2048 bits"}
		}
	case ed25519.PublicKey:
	default:
		return &InvalidCSRError{Reason: "its key type is not ECDSA, RSA or Ed25519"}
	}
	return nil
}

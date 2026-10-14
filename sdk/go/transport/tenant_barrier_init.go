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
	"crypto/tls"
	"crypto/x509"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/initproof"
)

// TenantBarrierInitProof is the proof of possession a network transport's
// init/complete call carries: begin's nonce, the DER SubjectPublicKeyInfo of
// the first admin's key, and that key's signature over the tenant, the nonce
// and the connection's TLS exporter (pkg/initproof). All three are empty
// when the node asked no proof.
type TenantBarrierInitProof struct {
	Nonce     []byte
	PublicKey []byte
	Signature []byte
}

// InitSignerRequiredError reports a tenant barrier init the node asked to be
// proved by the first admin's key, made without a signer to prove it with
// (TenantBarrierInitRequest.Signer).
type InitSignerRequiredError struct {
	TenantID string
}

// Error returns a human-readable description.
func (e *InitSignerRequiredError) Error() string {
	return "transport: tenant barrier init for " + strconv.Quote(e.TenantID) +
		" needs the first admin's key to prove possession, and no signer was given"
}

// InitPublicKeyError reports a signer whose public key cannot be encoded as a
// SubjectPublicKeyInfo.
type InitPublicKeyError struct {
	Err error
}

// Error returns a human-readable description.
func (e *InitPublicKeyError) Error() string {
	return "transport: tenant barrier init public key: " + e.Err.Error()
}

// Unwrap returns the encoding failure.
func (e *InitPublicKeyError) Unwrap() error { return e.Err }

// ProveTenantBarrierInit answers the nonce init/begin returned for tenantID,
// over the connection whose client-side TLS state is cs, with signer -- the
// first admin's key, which may be held by a device behind crypto.Signer. An
// empty nonce means the node asked no proof (an unauthenticated deployment),
// and the empty proof is returned. Otherwise a nil signer is refused with
// *InitSignerRequiredError, and a connection without a TLS 1.3 exporter with
// *initproof.NoExporterError.
func ProveTenantBarrierInit(signer crypto.Signer, tenantID string, nonce []byte, cs *tls.ConnectionState) (*TenantBarrierInitProof, error) {
	if len(nonce) == 0 {
		return &TenantBarrierInitProof{}, nil
	}
	if signer == nil {
		return nil, &InitSignerRequiredError{TenantID: tenantID}
	}
	pub, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return nil, &InitPublicKeyError{Err: err}
	}
	exporter, err := initproof.Exporter(cs)
	if err != nil {
		return nil, err
	}
	sig, err := initproof.Sign(signer, tenantID, nonce, exporter)
	if err != nil {
		return nil, err
	}
	return &TenantBarrierInitProof{Nonce: nonce, PublicKey: pub, Signature: sig}, nil
}

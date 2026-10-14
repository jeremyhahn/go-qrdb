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

// Package initproof is the proof of possession a tenant's first admin
// presents to initialize the tenant's barrier.
//
// The protocol is the officer-claim pattern bound to the connection: begin
// issues a single-use nonce on a TLS 1.3 connection, and complete carries a
// signature, by the key whose SPKI the pending first admin records, over the
// tenant id, that nonce and the TLS exporter (RFC 8446 section 7.5) of the
// connection it arrives on. A signature lifted off one connection and replayed
// on another covers the wrong exporter and fails, and so does a signature by
// any other key.
//
// Nothing here is new cryptography: the exporter is crypto/tls's
// ConnectionState.ExportKeyingMaterial and the signature is
// authfactors/mtls's SignNonce and VerifyNonceSignature, the same encoding the
// officer claim uses. The package is a leaf so the SDK transports, which
// compute the client side of the proof, and the auth middleware, which checks
// it, share one definition of the message.
package initproof

import (
	"crypto"
	"crypto/tls"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
)

const (
	// ExporterLabel is the RFC 5705 / RFC 8446 exporter label both ends of the
	// connection derive the binding with. The "EXPORTER-" prefix is the one
	// RFC 5705 section 4 reserves for application-defined labels.
	ExporterLabel = "EXPORTER-qrdb-tenant-barrier-init"

	// ExporterLength is the number of exporter bytes the proof covers.
	ExporterLength = 32

	// messageContext domain-separates the signed message from every other
	// signature the same key might be asked for -- an officer claim, a CSR,
	// a TLS handshake.
	messageContext = "qrdb-tenant-barrier-init-v1"
)

// Exporter returns the connection's keying material for the init proof.
//
// Only a completed TLS 1.3 handshake has an exporter this protocol accepts:
// TLS 1.2 exports only with Extended Master Secret, which the state does not
// report, and a connection without a handshake has none. A ConnectionState
// that was assembled rather than produced by crypto/tls -- the embedded
// transport's forwarded caller is one -- carries no keying material at all,
// and the standard library panics on it; that is answered as no exporter too.
func Exporter(cs *tls.ConnectionState) (exporter []byte, err error) {
	if cs == nil || !cs.HandshakeComplete || cs.Version != tls.VersionTLS13 {
		return nil, &NoExporterError{}
	}
	defer func() {
		if recover() != nil {
			exporter, err = nil, &NoExporterError{}
		}
	}()
	exporter, err = cs.ExportKeyingMaterial(ExporterLabel, nil, ExporterLength)
	if err != nil {
		return nil, &NoExporterError{Err: err}
	}
	return exporter, nil
}

// Message returns the bytes the first admin signs: the protocol context, the
// tenant id, the nonce and the exporter, the variable-length fields each
// terminated by a zero byte so no two inputs encode alike. Tenant ids cannot
// contain a zero byte (tenant.InvalidTenantIDCharError).
func Message(tenantID string, nonce, exporter []byte) []byte {
	msg := make([]byte, 0, len(messageContext)+len(tenantID)+len(nonce)+len(exporter)+2)
	msg = append(msg, messageContext...)
	msg = append(msg, 0)
	msg = append(msg, tenantID...)
	msg = append(msg, 0)
	msg = append(msg, nonce...)
	return append(msg, exporter...)
}

// Sign produces the proof signature with signer, which may hold its key on a
// device: only crypto.Signer's Sign is called, and the private key never
// leaves it. It returns *mtls.UnsupportedSignerError for a key type the
// verifier does not accept, and an empty nonce or exporter is refused the
// same way so no caller can sign a message that binds nothing.
func Sign(signer crypto.Signer, tenantID string, nonce, exporter []byte) ([]byte, error) {
	if len(nonce) == 0 || len(exporter) == 0 {
		return nil, &mtls.UnsupportedSignerError{}
	}
	return mtls.SignNonce(signer, Message(tenantID, nonce, exporter))
}

// Verify reports whether signature is pub's signature over the proof message
// for tenantID, nonce and exporter. An empty nonce or exporter never
// verifies.
func Verify(pub crypto.PublicKey, tenantID string, nonce, exporter, signature []byte) bool {
	if len(nonce) == 0 || len(exporter) == 0 {
		return false
	}
	return mtls.VerifyNonceSignature(pub, Message(tenantID, nonce, exporter), signature)
}

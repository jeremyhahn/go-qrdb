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

package initproof

import "strconv"

// Reason names why a proof was refused. The values are stable: they reach
// the caller in the error text and the audit trail.
type Reason string

const (
	// ReasonAbsent: the request carried no nonce, key or signature.
	ReasonAbsent Reason = "proof absent"
	// ReasonUnknownNonce: the nonce was never issued here, or was already
	// used -- a replay -- or was issued for another tenant.
	ReasonUnknownNonce Reason = "nonce unknown or already used"
	// ReasonExpired: the nonce outlived ChallengeTTL.
	ReasonExpired Reason = "nonce expired"
	// ReasonConnectionMismatch: the nonce was issued on a different
	// connection from the one completing it.
	ReasonConnectionMismatch Reason = "nonce issued on another connection"
	// ReasonNoExporter: the connection has no TLS 1.3 exporter to bind to.
	ReasonNoExporter Reason = "connection has no TLS exporter"
	// ReasonMalformedKey: the public key is not a parsable SPKI.
	ReasonMalformedKey Reason = "public key malformed"
	// ReasonWrongKey: the key is not the one the first admin was onboarded
	// with.
	ReasonWrongKey Reason = "key is not the first admin's"
	// ReasonBadSignature: the signature does not verify over this tenant,
	// nonce and connection.
	ReasonBadSignature Reason = "signature does not verify"
	// ReasonNoPendingAdmin: the tenant has no first admin to prove.
	ReasonNoPendingAdmin Reason = "tenant has no pending first admin"
)

// NoExporterError is returned by Exporter when the connection has no TLS 1.3
// keying material to bind a proof to.
type NoExporterError struct {
	// Err is crypto/tls's refusal, when it gave one.
	Err error
}

func (e *NoExporterError) Error() string {
	if e.Err != nil {
		return "initproof: connection has no TLS 1.3 exporter: " + e.Err.Error()
	}
	return "initproof: connection has no TLS 1.3 exporter"
}

func (e *NoExporterError) Unwrap() error { return e.Err }

// ChallengeError is returned by Book.Consume when a nonce cannot be redeemed.
type ChallengeError struct {
	Reason Reason
}

func (e *ChallengeError) Error() string {
	return "initproof: " + string(e.Reason)
}

// TooManyChallengesError is returned by Book.Issue when the node already holds
// its limit of unexpired challenges. Retrying after ChallengeTTL succeeds.
type TooManyChallengesError struct {
	Limit int
}

func (e *TooManyChallengesError) Error() string {
	return "initproof: " + strconv.Itoa(e.Limit) + " init challenges already outstanding"
}

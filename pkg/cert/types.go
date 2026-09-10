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

// Package cert provides pluggable certificate management with a default
// implementation that uses barrier-sealed CA key material. Client
// certificates are signed by an internal CA whose private key is
// encrypted at rest via the EncryptionBarrier and held in secure memory
// while unsealed.
package cert

import "time"

// GenerateRequest carries the parameters for client certificate generation.
type GenerateRequest struct {
	// Username sets the certificate Common Name (CN).
	Username string

	// TenantID sets the certificate Organizational Unit (OU).
	TenantID string

	// TTL is the certificate validity period. Zero means the provider's
	// default TTL is used.
	TTL time.Duration
}

// CertBundle holds the PEM-encoded certificate and private key returned
// after generating a client certificate.
type CertBundle struct {
	// CertPEM is the PEM-encoded X.509 client certificate.
	CertPEM []byte

	// KeyPEM is the PEM-encoded ECDSA private key.
	KeyPEM []byte

	// Serial is the hexadecimal serial number of the certificate.
	Serial string
}

// RevokeOutcome reports what RevokeCert (or RevokeCertResult) found when it
// wrote a revocation marker for a serial. Both values are SUCCESSES: the
// marker is written unconditionally, before either outcome is determined, so
// the outcome describes what was found, never what was done. See
// RevokeResult and go-qrdb#287.
type RevokeOutcome int

const (
	// RevokeOutcomeRecordUpdated means this node held a CertRecord for the
	// serial, and its Revoked display field was updated alongside the
	// marker.
	RevokeOutcomeRecordUpdated RevokeOutcome = iota

	// RevokeOutcomeDenyBySerial means no local CertRecord matched the
	// serial -- the certificate may have been issued by a peer, or its
	// record pruned. The marker was still written: this is a deliberate,
	// successful deny-by-serial revocation, not a failure. See "Deny-by
	// serial" in docs/certificate-authority/architecture.md.
	RevokeOutcomeDenyBySerial
)

// String renders the outcome for logs and CLI output.
func (o RevokeOutcome) String() string {
	switch o {
	case RevokeOutcomeRecordUpdated:
		return "record_updated"
	case RevokeOutcomeDenyBySerial:
		return "deny_by_serial"
	default:
		return "unknown"
	}
}

// RevokeResult reports what a revocation did, distinguishing a revocation of
// a certificate this node holds a record for from a deny-by-serial
// revocation of one it does not -- the distinction RevokeCert's plain error
// return cannot express without lying about one of the two cases (both are
// successes). See RevokeOutcome.
type RevokeResult struct {
	// Serial is the revoked certificate serial, echoing the request.
	Serial string

	// Outcome reports whether a local CertRecord matched.
	Outcome RevokeOutcome
}

// RecordFound reports whether this node held a CertRecord for the revoked
// serial. It is the bool projection of Outcome that transports serialize,
// since the two-value enum only exists in Go call sites.
func (r *RevokeResult) RecordFound() bool {
	return r.Outcome == RevokeOutcomeRecordUpdated
}

// CertRecord is the metadata stored in KVStore for each issued certificate.
type CertRecord struct {
	// Serial is the hexadecimal serial number of the certificate.
	Serial string `json:"serial"`

	// Username is the Common Name (CN) of the certificate subject.
	Username string `json:"username"`

	// TenantID is the Organizational Unit (OU) of the certificate subject.
	TenantID string `json:"tenantID"`

	// IssuedAt is the time the certificate was issued.
	IssuedAt time.Time `json:"issuedAt"`

	// ExpiresAt is the time the certificate expires.
	ExpiresAt time.Time `json:"expiresAt"`

	// Revoked indicates whether the certificate has been revoked.
	Revoked bool `json:"revoked"`
}

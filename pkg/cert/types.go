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

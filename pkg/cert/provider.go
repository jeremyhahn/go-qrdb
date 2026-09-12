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

import "context"

// CertProvider is the pluggable certificate management interface.
// Implementations generate client certificates signed by a CA, manage
// revocation, and export certificates for browser-based mTLS.
type CertProvider interface {
	// GenerateClientCert creates a client certificate signed by the CA.
	GenerateClientCert(ctx context.Context, req *GenerateRequest) (*CertBundle, error)

	// ExportPKCS12 exports a certificate and key as PKCS#12 (.p12)
	// for browser import.
	ExportPKCS12(ctx context.Context, certPEM, keyPEM []byte, passphrase string) ([]byte, error)

	// RevokeCert marks a certificate serial as revoked and reports what it
	// found: RevokeResult.Outcome distinguishes a certificate this node
	// holds a record for from a deny-by-serial revocation of one it does
	// not. Both are successes -- the marker is written unconditionally,
	// before the outcome is even determined -- which is why the
	// distinction is carried in the result rather than the error return.
	//
	// BREAKING (go-qrdb#287): this method previously returned only error.
	RevokeCert(ctx context.Context, serial string) (*RevokeResult, error)

	// IsRevoked checks whether a certificate serial is revoked.
	IsRevoked(ctx context.Context, serial string) (bool, error)

	// TenantForSerial reports which tenant owns the issued certificate
	// bearing the given serial, and whether this node holds such a record.
	//
	// RevokeCert takes only a serial, and a serial carries no tenant, so
	// this is the only way an authorization layer can establish the TARGET
	// of a revocation rather than assuming the caller's own tenant. found ==
	// false is the deny-by-serial case (permission.CertsRevokeCross).
	TenantForSerial(ctx context.Context, serial string) (tenantID string, found bool, err error)

	// ListCerts lists all issued client certificates for a tenant.
	ListCerts(ctx context.Context, tenantID string) ([]*CertRecord, error)

	// CACertPEM returns the PEM-encoded CA certificate for client
	// verification.
	CACertPEM() []byte
}

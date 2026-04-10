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

	// RevokeCert marks a certificate serial as revoked.
	RevokeCert(ctx context.Context, serial string) error

	// IsRevoked checks whether a certificate serial is revoked.
	IsRevoked(ctx context.Context, serial string) (bool, error)

	// ListCerts lists all issued client certificates for a tenant.
	ListCerts(ctx context.Context, tenantID string) ([]*CertRecord, error)

	// CACertPEM returns the PEM-encoded CA certificate for client
	// verification.
	CACertPEM() []byte
}

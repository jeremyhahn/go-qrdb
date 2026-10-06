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

// Package trustanchor holds each tenant's registered trust anchors: the CA
// certificates -- an enterprise PKI's roots, typically -- that a tenant
// accepts for its own users' bring-your-own login certificates.
//
// An anchor vouches for ITS tenant only. It is added to the client-facing
// listeners' trust pool (pkg/clienttrust) so a certificate chaining to it
// completes a TLS handshake, and the issuer-tenant binding then admits that
// certificate only as a user of the tenant that registered the anchor. A
// tenant registering a public root therefore widens its own login surface and
// nobody else's.
//
// The seam is Registry. Its read side, Source, is the shape of go-xkms's
// resolver.CABundleProvider -- a PEM CA bundle -- with the tenant made
// explicit, so a go-xkms composer injects its own trust store through a thin
// adapter (a per-tenant CABundleProvider, or a TrustedCA's CABundleCertPool
// encoded to PEM). The built-in implementation, NewKVRegistry, keeps each
// tenant's anchors in the replicated system store, inside that tenant's
// identity namespace (tenant.IdentityNamespace), where they stay readable
// while the tenant's own barrier is sealed -- a BYOC tenant admin must be able
// to log in to unseal it.
package trustanchor

import (
	"context"
	"time"
)

// Anchor is one registered trust anchor.
type Anchor struct {
	// Fingerprint is the lowercase hex SHA-256 of the certificate's DER
	// encoding. It is the anchor's identifier: Remove takes it.
	Fingerprint string

	// Subject is the certificate's Subject, for display.
	Subject string

	// NotBefore and NotAfter are the certificate's validity window.
	NotBefore time.Time
	NotAfter  time.Time

	// CertPEM is the PEM-encoded certificate.
	CertPEM []byte

	// AddedAt is when the anchor was registered. Zero for an injected
	// Registry that does not record it.
	AddedAt time.Time
}

// Source supplies one tenant's registered trust anchors.
//
// CABundle returns the anchors as a concatenated PEM bundle, empty (nil, nil)
// when the tenant has none. It is go-xkms's resolver.CABundleProvider with the
// tenant explicit: every trust decision qrdb makes about a tenant's anchors
// reads them through this one method.
type Source interface {
	CABundle(ctx context.Context, tenantID string) ([]byte, error)
}

// Registry is a Source that a tenant admin can manage.
//
// Add validates and registers every certificate in a PEM bundle (each must be
// a CA certificate inside its validity window) and returns them as
// registered; re-adding a registered anchor is not an error. List returns a
// tenant's anchors. Remove withdraws one by fingerprint and returns
// *AnchorNotFoundError when the tenant has no such anchor.
//
// An implementation whose anchors are managed elsewhere -- an injected go-xkms
// trust store, say -- returns *ExternallyManagedError from Add and Remove.
type Registry interface {
	Source
	Add(ctx context.Context, tenantID string, bundlePEM []byte) ([]Anchor, error)
	List(ctx context.Context, tenantID string) ([]Anchor, error)
	Remove(ctx context.Context, tenantID, fingerprint string) error
}

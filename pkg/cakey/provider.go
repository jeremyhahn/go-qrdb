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

// Package cakey defines the single seam through which every certificate
// authority in go-qrdb obtains its signing key and certificate: the cluster
// CA (which also backs the ceremony CA and node-leaf issuance) and the login
// CA (pkg/cert). Nothing outside this package ever holds a CA private key
// directly -- every signature is produced by calling Provider.Signer and
// using the returned crypto.Signer, never by reading key bytes out of it.
//
// go-qrdb is embedded as the datastore and consensus engine under go-xkms
// and other projects that bring their own key custody (software, TPM 2.0,
// PKCS#11, cloud KMS). Provider is shaped so that an adapter over go-xkms's
// pkg/ca.CASigningIdentity (Signer) and pkg/ca.CACertificateAccessor
// (Certificate) satisfies it with nothing more than Adapter -- go-qrdb never
// imports go-xkms, so the fit is verified here with a same-shaped test
// double, not with the real type.
package cakey

import (
	"context"
	"crypto"
	"crypto/x509"
)

// Provider is the minimal seam a CA needs to sign something: a
// crypto.Signer backed by the CA's private key, and the CA's own
// certificate. It never exposes the private key itself.
//
// Thread-safe: implementations must support concurrent calls from every
// signing path (client-cert issuance, ceremony CSR signing, node-leaf
// issuance, member-side cluster-join CSR signing).
type Provider interface {
	// Signer returns a crypto.Signer backed by the CA's private key. With a
	// hardware or remote backend (HSM, TPM, KMS) the returned Signer routes
	// Sign calls to that device; the key material itself never enters this
	// process. Returns *BarrierSealedError when the key is barrier-protected
	// and the barrier is currently sealed, and *NotInitializedError when no
	// key has ever been established.
	Signer(ctx context.Context) (crypto.Signer, error)

	// Certificate returns the CA's own certificate. Unlike Signer, this is
	// public data and implementations are encouraged to serve it even while
	// otherwise sealed, mirroring cert.CertProvider.CACertPEM's contract.
	Certificate(ctx context.Context) (*x509.Certificate, error)
}

// Adapter composes two independently-sourced functions into a Provider. It
// is the "no glue" seam an embedder's own CA type is wired through: an
// xkms-backed CA implements pkg/ca.CASigningIdentity.Signer and
// pkg/ca.CACertificateAccessor.Certificate (closed over its own
// types.KeyAttributes), and those two methods alone are enough to build an
// Adapter without copying any xkms type into go-qrdb.
type Adapter struct {
	// SignerFunc produces the signer. Required.
	SignerFunc func(ctx context.Context) (crypto.Signer, error)

	// CertificateFunc produces the certificate. Required.
	CertificateFunc func(ctx context.Context) (*x509.Certificate, error)
}

// Compile-time interface check.
var _ Provider = Adapter{}

// CertOnlyProvider is the capability a Provider MAY additionally offer: its
// certificate resolved without ever establishing one as a side effect of
// being asked. A caller on a path that must never generate a CA -- a TLS
// handshake resolving trust, never an administrator-invoked operation --
// type-asserts a Provider against this interface and uses CertificateOnly
// when it is satisfied, falling back to the ordinary Certificate otherwise.
// The fallback is safe for every Provider in this package that does NOT
// implement CertOnlyProvider: StaticProvider's Certificate already never
// generates anything (it wraps a certificate the caller already supplied),
// and the same is true of an Adapter over an externally-managed CA (an
// xkms-backed hardware or remote provider establishes its own material on
// its own schedule, never lazily in response to a Certificate call this
// process makes).
//
// *SoftwareProvider is the one Provider in this package whose Certificate
// DOES establish a CA as a side effect on a cold cache (see its own doc
// comment), which is exactly why it implements this interface: see
// CertificateOnly.
type CertOnlyProvider interface {
	// CertificateOnly resolves the certificate without ever creating one.
	CertificateOnly(ctx context.Context) (*x509.Certificate, error)
}

// Compile-time interface check.
var _ CertOnlyProvider = (*SoftwareProvider)(nil)

// Signer calls SignerFunc.
func (a Adapter) Signer(ctx context.Context) (crypto.Signer, error) {
	return a.SignerFunc(ctx)
}

// Certificate calls CertificateFunc.
func (a Adapter) Certificate(ctx context.Context) (*x509.Certificate, error) {
	return a.CertificateFunc(ctx)
}

// ProviderFactory resolves the Provider that signs a SPECIFIC tenant's Key B
// (login CA) material. An embedder's hardware or remote backend (HSM, TPM,
// KMS) implements this, rather than injecting a single bare Provider shared
// across every tenant, whenever it backs more than one tenant: see
// pkg/cert.WithKeyProviderFactory.
//
// The built-in default (no factory injected) already resolves this way
// itself -- a fresh cakey.SoftwareProvider per tenant, its own storage
// prefix and Subject, never shared -- and an injected factory must do the
// same, one key per tenant (key ID = tenant), for the issuer-tenant binding
// (pkg/clienttrust.Pool.VerifyIssuer, via CertSource.IsTenantIssuer) to mean
// anything. A single Provider shared across every tenant makes that binding
// vacuous: every tenant's presented certificate would verify against the
// SAME CA, so IsTenantIssuer would answer true for whichever tenant the
// certificate's Subject OU merely CLAIMED, regardless of which tenant the
// signing key was actually provisioned for (go-qrdb#591's review, owner
// ruling). There is deliberately no adapter in this package that lifts a
// single Provider into a ProviderFactory by ignoring the tenant argument:
// that is precisely the shape this type exists to make impossible to reach
// by accident.
//
// TenantIDs additionally names every tenant the factory currently backs, so
// a caller enumerating trust (pkg/cert.DefaultCertProvider.CACertPEMs, which
// the dynamic client-auth trust pool widens ClientCAs from) can discover and
// load each one's certificate in turn -- qrdb's own kvStore has no record of
// a factory-backed tenant's CA at all, unlike the built-in software path,
// where that same discovery is a prefix scan over records this package
// wrote itself.
type ProviderFactory interface {
	// ProviderForTenant returns the Provider that signs tenantID's Key B
	// material -- its own key, never one any other tenant's logins also
	// resolve to.
	ProviderForTenant(tenantID string) (Provider, error)

	// TenantIDs returns every tenant ID this factory currently backs.
	TenantIDs(ctx context.Context) ([]string, error)
}

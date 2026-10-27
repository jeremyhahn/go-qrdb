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

package sdk

import (
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/clienttrust"
)

// Certificate revocation is enforced on the authentication path of every
// transport, on every request. These aliases are the seam an embedder needs
// when it assembles its own authentication chain; a node built through
// node.NewStandaloneNode / NewClusteredNode wires enforcement itself from its
// certificate provider and DaemonAuthConfig.RevocationCacheTTL.

// ---------------------------------------------------------------------------
// Login CA seam
//
// The login CA (pkg/cert.CertProvider) issues, revokes and lists the
// client certificates users authenticate with. An embedder with its own CA --
// one that publishes CRLs and answers OCSP -- implements ClientCertProvider and
// passes it to node.WithCertProvider. The SDK name CertProvider is the
// first-boot admin-credential seam (firstboot.CertProvider), so this contract
// is published under the ClientCert prefix.
// ---------------------------------------------------------------------------

// ClientCertProvider is the login CA contract. (Aliases
// pkg/cert.CertProvider.)
type ClientCertProvider = cert.CertProvider

// ClientCertIssueRequest asks a ClientCertProvider to sign a requester's own
// public key; the provider never generates a client key. (Aliases
// pkg/cert.IssueRequest.)
type ClientCertIssueRequest = cert.IssueRequest

// ClientCertIssued is a signed client certificate, without a private key.
// (Aliases pkg/cert.IssuedCert.)
type ClientCertIssued = cert.IssuedCert

// ClientCertRevokeResult reports what a revocation found. (Aliases
// pkg/cert.RevokeResult.)
type ClientCertRevokeResult = cert.RevokeResult

// ClientCertRevokeOutcome distinguishes a revoked certificate the provider
// holds a record for from a deny-by-serial revocation; both are successes.
// (Aliases pkg/cert.RevokeOutcome.)
type ClientCertRevokeOutcome = cert.RevokeOutcome

// Revocation outcomes.
const (
	// ClientCertRevokeRecordUpdated says the provider held a record for the
	// serial.
	ClientCertRevokeRecordUpdated = cert.RevokeOutcomeRecordUpdated
	// ClientCertRevokeDenyBySerial says no record matched and the serial was
	// denied anyway.
	ClientCertRevokeDenyBySerial = cert.RevokeOutcomeDenyBySerial
)

// ClientCertRecord is the metadata of one issued client certificate.
// (Aliases pkg/cert.CertRecord.)
type ClientCertRecord = cert.CertRecord

// ClientCertSource is the capability a ClientCertProvider must also offer for
// the node's client-trust pool to admit the certificates it issues: every
// tenant CA's certificate, and whether an issuer is a given tenant's CA. A
// provider without it issues certificates no node accepts. (Aliases
// pkg/clienttrust.CertSource.)
type ClientCertSource = clienttrust.CertSource

// ClientCertInvalidationSource is the capability a ClientCertProvider may
// offer to tell the client-trust pool its tenants' CAs changed, so a new or
// rotated CA is trusted without waiting for the pool's cache to expire.
// (Aliases pkg/clienttrust.InvalidationSource.)
type ClientCertInvalidationSource = clienttrust.InvalidationSource

// EncodePKCS12 bundles a certificate, its private key and the CA certificate
// into a passphrase-protected PKCS#12 archive, for importing an issued
// identity into a browser or operating-system key store.
var EncodePKCS12 = cert.EncodePKCS12

// ---------------------------------------------------------------------------
// Certificate revocation enforcement
// ---------------------------------------------------------------------------

// RevocationChecker is the contract the authentication layer consults on
// EVERY request to decide whether a verified peer certificate has been
// revoked.
//
// It is deliberately not a TLS handshake callback: a handshake hook fires once
// per connection, so on QUIC a peer revoked mid-session would stay authorised
// for the life of its connection.
type RevocationChecker = auth.RevocationChecker

// RevocationCache is the standard RevocationChecker: a bounded-TTL cache over
// a CertProvider. Build one with NewRevocationCache.
type RevocationCache = cert.RevocationCache

// NewRevocationCache builds the standard RevocationChecker over a
// CertProvider.
//
// ttl is the lifetime of a cached NOT-REVOKED answer, and it is a SECURITY
// WINDOW: the maximum time a certificate that has just been revoked continues
// to authenticate on a node that had already seen it. Zero disables caching so
// every request consults replicated state. A REVOKED answer is cached without
// expiry, because revocation in go-qrdb is a one-way door.
//
// An embedder running a node through node.NewStandaloneNode /
// NewClusteredNode does NOT need this: the node wires enforcement from its
// certificate provider and DaemonAuthConfig.RevocationCacheTTL. It is exported
// for a caller assembling its own authentication chain.
var NewRevocationCache = cert.NewRevocationCache

// DefaultRevocationCacheTTL is the default not-revoked cache lifetime, and so
// the default width of the window described at NewRevocationCache.
const DefaultRevocationCacheTTL = cert.DefaultRevocationCacheTTL

// WithRevocationChecker enables per-request certificate-revocation
// enforcement on an mTLS authenticator assembled by hand.
var WithRevocationChecker = auth.WithRevocationChecker

// ErrCertificateRevoked is the authentication failure returned when a peer
// certificate verified against the CA but its serial has been revoked.
var ErrCertificateRevoked = auth.ErrCertificateRevoked

// RevocationCheckError reports that a certificate's revocation status could
// not be determined. It is a REFUSAL rather than a denial — transports map it
// to 503 / UNAVAILABLE so a client retries — and it is how the authentication
// path fails CLOSED when replicated state cannot be read.
type RevocationCheckError = auth.RevocationCheckError

// RevocationStateUnavailableError says no revocation answer exists on this
// node at all, because the barrier is sealed. It is distinct from a failed
// lookup: the authentication layer admits such a request, since refusing every
// mTLS caller while sealed would make the barrier unsealable.
type RevocationStateUnavailableError = cert.RevocationStateUnavailableError

// RevocationStateUnavailable reports whether err carries that distinction.
var RevocationStateUnavailable = auth.RevocationStateUnavailable

// FormatSerial renders a certificate serial in the canonical form the CA
// stores and compares by. It is the single definition of that format;
// deriving it independently anywhere is how an enforcement lookup silently
// matches nothing.
var FormatSerial = cert.FormatSerial

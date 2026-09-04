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
)

// Certificate revocation is enforced on the authentication path of every
// transport, on every request. These aliases are the seam an embedder needs
// when it assembles its own authentication chain; a node built through
// node.NewStandaloneNode / NewClusteredNode wires enforcement itself from its
// certificate provider and DaemonAuthConfig.RevocationCacheTTL.
//
// The full pkg/cert.CertProvider contract is deliberately NOT aliased here:
// the SDK already binds the name CertProvider to firstboot.CertProvider (the
// admin-mTLS Path A seam), and silently rebinding a published name would break
// every consumer that uses it. Closing that gap needs a naming decision, not a
// second alias.

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

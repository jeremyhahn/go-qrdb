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
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/initproof"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Signed tenant barrier initialization: the server side
// ---------------------------------------------------------------------------
//
// transport.ProveTenantBarrierInit is the client's half of init/begin and
// init/complete. The aliases below are the server's half, for a facade that
// terminates the caller's TLS itself (its own REST, gRPC or MCP service) and
// so has to run the exchange on its own connection: begin issues a nonce
// bound to the tenant and the connection's TLS exporter, complete checks the
// proof against that nonce, that exporter and the expected key and consumes
// the nonce. They are the code every qrdb transport runs, not a copy of it.

// TenantBarrierInitVerifier holds the book of init nonces and verifies init
// proofs: Begin(tenantID, tlsState) and Complete(tenantID, tlsState, proof,
// key). It decides the proof only; authorizing the caller to begin or
// complete is the facade's. (Aliases
// pkg/auth/middleware.TenantBarrierInitVerifier.)
type TenantBarrierInitVerifier = authmw.TenantBarrierInitVerifier

// NewTenantBarrierInitVerifier returns a verifier over book; nil is a fresh
// book on the real clock with InitChallengeMaxOutstanding.
var NewTenantBarrierInitVerifier = authmw.NewTenantBarrierInitVerifier

// TenantBarrierInitKey returns the SPKI thumbprint (ComputeSPKIThumbprint) of
// the key the proof must be by -- the tenant's pending first admin's, or the
// certificate's on the connection. Empty refuses the proof
// InitProofReasonNoPendingAdmin.
type TenantBarrierInitKey = authmw.TenantBarrierInitKey

// TenantBarrierInitProof is the proof a caller presents at init/complete: the
// nonce, the DER SubjectPublicKeyInfo of its key and the signature.
type TenantBarrierInitProof = authmw.TenantBarrierInitProof

// InitChallenge is the nonce init/begin issues, with its expiry.
type InitChallenge = initproof.Challenge

// InitChallengeBook is the bounded, single-use book of init nonces a
// verifier issues from and redeems against.
type InitChallengeBook = initproof.Book

// NewInitChallengeBook returns a book on clk (nil is the real clock) holding
// at most limit unexpired challenges (0 or less is
// InitChallengeMaxOutstanding).
var NewInitChallengeBook = initproof.NewBook

// The init challenge bounds.
const (
	// InitChallengeTTL is how long an issued nonce can be redeemed.
	InitChallengeTTL = initproof.ChallengeTTL
	// InitChallengeMaxOutstanding is a book's default limit of unexpired
	// challenges.
	InitChallengeMaxOutstanding = initproof.DefaultMaxOutstanding
)

// FirstAdminProofError is every refusal of an init proof; Reason names which
// check failed.
type FirstAdminProofError = tenant.FirstAdminProofError

// InitProofReason names why an init proof was refused.
type InitProofReason = initproof.Reason

// The InitProofReason values.
const (
	InitProofReasonAbsent             = initproof.ReasonAbsent
	InitProofReasonUnknownNonce       = initproof.ReasonUnknownNonce
	InitProofReasonExpired            = initproof.ReasonExpired
	InitProofReasonConnectionMismatch = initproof.ReasonConnectionMismatch
	InitProofReasonNoExporter         = initproof.ReasonNoExporter
	InitProofReasonMalformedKey       = initproof.ReasonMalformedKey
	InitProofReasonWrongKey           = initproof.ReasonWrongKey
	InitProofReasonBadSignature       = initproof.ReasonBadSignature
	InitProofReasonNoPendingAdmin     = initproof.ReasonNoPendingAdmin
)

// InitTooManyChallengesError refuses a begin while the book holds its limit
// of unexpired challenges.
type InitTooManyChallengesError = initproof.TooManyChallengesError

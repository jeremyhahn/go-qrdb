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

// Single-use, identity-bound join-admission tokens. This replaces the
// earlier static shared-secret design (a single token string, configured
// identically on the signing member and every joiner, checked with
// subtle.ConstantTimeCompare): any node holding the shared secret could
// request a leaf for ANY identity, including one already admitted, and
// the secret never expired or was consumed. Coordinator decision,
// following the 2026-09-30 "single-use replica-bound token" ruling:
//
//   - An admin mints one token per joining node, bound to that node's
//     SPIFFE node identity (trust domain + node id) and an expiry.
//   - Only the token's SHA-256 digest is ever stored, as the replicated
//     store's own key -- the plaintext is returned exactly once, to the
//     minting caller, and never persisted anywhere (pkg/invite's own
//     invitation tokens follow the identical rule, and GenerateToken/
//     TokenDigest are reused from there rather than reimplemented).
//   - The token is consumed atomically (CompareAndDelete) on first use:
//     a second presentation of the same token, concurrent or not, is
//     refused exactly as an unknown token is.
//   - The leaf clusterca.IssueLeafForKey issues is for the BOUND identity
//     the token names, never the joiner's self-reported NodeURI (B6/B5).
package clusterenroll

import (
	"context"
	"encoding/json"
	"net/url"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/invite"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// joinTokenPrefix is the replicated-store namespace join-admission tokens
// live under, keyed by the hex SHA-256 digest of the plaintext token --
// never the plaintext itself, and never barrier-wrapped on top of that:
// the digest is already unguessable (invite.TokenDigest, 256 bits of
// uniform randomness underneath it), so a second encryption layer would
// repeat B1's own mistake of adding protection that is actually a
// liability once the record is replicated.
const joinTokenPrefix = "__sys:cluster_join_tokens/"

// joinIdentityPrefix records, per admitted node identity, that a join
// token has already been consumed for it. Entries are never removed: a
// SECOND join attempt presenting a (new, validly minted) token for an
// identity that has already joined is refused rather than silently
// re-admitted -- "refuse identities of existing members" -- because this
// package cannot tell "the first leaf never reached the joiner" apart from
// "an attacker is attempting to mint a second identity on top of an
// admitted one" from here; an operator re-admitting a genuinely lost node
// mints a token for a NEW node id, which is a deliberate, auditable action.
const joinIdentityPrefix = "__sys:cluster_join_identities/"

// Join-token TTL bounds. A token with no expiry would be the static
// secret's own defect restated; one bounded to seconds would make an
// operator's minting-to-delivery window a race. These are deliberately
// generous (an operator copies a token to a new node by hand) while still
// bounding a leaked, unused token's lifetime.
const (
	// MinJoinTokenTTL is the shortest expiry MintJoinToken accepts.
	MinJoinTokenTTL = time.Minute

	// MaxJoinTokenTTL is the longest expiry MintJoinToken accepts.
	MaxJoinTokenTTL = 24 * time.Hour

	// DefaultJoinTokenTTL is used when MintJoinToken is called with ttl <= 0.
	DefaultJoinTokenTTL = time.Hour
)

// joinTokenRecord is the JSON envelope stored under joinTokenPrefix+digest.
// NodeURI is the canonical spiffe://<trust-domain>/node/<nodeID> string the
// resulting leaf's URI SAN will carry -- never anything a joiner supplies.
type joinTokenRecord struct {
	NodeURI   string    `json:"node_uri"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// MintJoinToken generates a single-use join-admission token bound to the
// node identity spiffe://trustDomain/node/nodeID, persists only its SHA-256
// digest (via kvstore.AtomicKVStore.PutIfAbsent, so a vanishingly unlikely
// digest collision with an existing token is refused rather than silently
// overwritten), and returns the plaintext token exactly once. Callers never
// see it again: there is no "list minted tokens" read path, by the same
// write-only-credential reasoning every other admission secret in this
// codebase follows.
//
// ttl <= 0 uses DefaultJoinTokenTTL; a ttl outside
// [MinJoinTokenTTL, MaxJoinTokenTTL] is refused with *JoinTokenTTLError.
func MintJoinToken(ctx context.Context, store kvstore.AtomicKVStore, trustDomain, nodeID string, ttl time.Duration) (token string, expiresAt time.Time, err error) {
	identity := spiffe.NodeIdentity{TrustDomain: trustDomain, NodeID: nodeID}
	if err := identity.Validate(); err != nil {
		return "", time.Time{}, &InvalidJoinIdentityError{Err: err}
	}

	if ttl <= 0 {
		ttl = DefaultJoinTokenTTL
	}
	if ttl < MinJoinTokenTTL || ttl > MaxJoinTokenTTL {
		return "", time.Time{}, &JoinTokenTTLError{TTL: ttl}
	}

	token, err = invite.GenerateToken()
	if err != nil {
		return "", time.Time{}, &JoinTokenGenerateError{Err: err}
	}
	digest := invite.TokenDigest(token)

	now := time.Now().UTC()
	expiresAt = now.Add(ttl)
	record := joinTokenRecord{NodeURI: identity.String(), ExpiresAt: expiresAt, CreatedAt: now}
	data, err := json.Marshal(record)
	if err != nil {
		return "", time.Time{}, &JoinTokenGenerateError{Err: err}
	}

	key := joinTokenPrefix + digest
	stored, err := store.PutIfAbsent(ctx, key, data)
	if err != nil {
		return "", time.Time{}, &JoinTokenStoreError{Op: "putifabsent", Detail: key, Err: err}
	}
	if !stored {
		return "", time.Time{}, &JoinTokenCollisionError{}
	}
	return token, expiresAt, nil
}

// ConsumeJoinToken verifies and atomically consumes a presented join
// token, returning the SPIFFE node identity it is bound to -- the ONLY
// identity the resulting leaf may be issued for. Every failure, whatever
// the specific cause (unknown digest, lost the single-use race, expired,
// wrong trust domain, malformed stored record, identity already admitted),
// returns the IDENTICAL *JoinRefusedError so a caller probing tokens
// cannot distinguish "wrong" from "right but something else failed" from
// the response alone (no token-validity oracle).
//
// The token is consumed (CompareAndDelete) BEFORE any of the later checks
// run: a token that turns out to be expired, or bound to the wrong trust
// domain, is still spent by having been presented, so retrying it against
// a different trust domain or after waiting out a clock skew gains an
// attacker nothing.
//
// This does NOT mark the identity admitted -- it only refuses a token
// bound to an identity ALREADY marked (a read-only check). Call
// MarkJoinIdentityAdmitted once the caller has actually issued a leaf; see
// HandleJoinRequest's doc comment for why that is deliberately a separate,
// later step.
func ConsumeJoinToken(ctx context.Context, store kvstore.AtomicKVStore, trustDomain, token string) (boundNodeURI string, err error) {
	if token == "" {
		return "", errJoinTokenRejected
	}
	digest := invite.TokenDigest(token)
	key := joinTokenPrefix + digest

	data, err := store.Get(ctx, key)
	if err != nil {
		return "", &JoinTokenStoreError{Op: "get", Detail: key, Err: err}
	}
	if data == nil {
		return "", errJoinTokenRejected
	}

	var record joinTokenRecord
	if err := json.Unmarshal(data, &record); err != nil {
		// The record this process is about to try to consume is corrupt;
		// still attempt the delete so a corrupt record is not left behind
		// forever, but report the same refusal either way.
		_, _ = store.CompareAndDelete(ctx, key, data)
		return "", errJoinTokenRejected
	}

	deleted, err := store.CompareAndDelete(ctx, key, data)
	if err != nil {
		return "", &JoinTokenStoreError{Op: "compareanddelete", Detail: key, Err: err}
	}
	if !deleted {
		// Lost a concurrent race to consume the SAME token.
		return "", errJoinTokenRejected
	}

	if time.Now().After(record.ExpiresAt) {
		return "", errJoinTokenRejected
	}

	u, err := url.Parse(record.NodeURI)
	if err != nil {
		return "", errJoinTokenRejected
	}
	identity, ok, err := spiffe.ParseNodeID(u)
	if err != nil || !ok {
		// Either a malformed node path, or (ok==false) some other SPIFFE
		// path shape entirely -- a tenant path, most notably. A join token
		// binds a NODE identity and nothing else.
		return "", errJoinTokenRejected
	}
	if identity.TrustDomain != trustDomain {
		return "", errJoinTokenRejected
	}

	identityKey := joinIdentityPrefix + identity.String()
	alreadyAdmitted, err := store.Exists(ctx, identityKey)
	if err != nil {
		return "", &JoinTokenStoreError{Op: "exists", Detail: identityKey, Err: err}
	}
	if alreadyAdmitted {
		return "", errJoinTokenRejected
	}

	return identity.String(), nil
}

// MarkJoinIdentityAdmitted records that nodeURI has been admitted through
// the member-side CSR join mechanism, so a later join attempt presenting a
// (new, validly minted) token for the same identity is refused by
// ConsumeJoinToken rather than silently re-admitted. Called by
// HandleJoinRequest ONLY after IssueLeafForKey has actually issued a leaf
// -- never bundled with ConsumeJoinToken's token consumption -- so that a
// signing failure after the token is consumed leaves the identity free for
// a retry with a freshly minted token. See HandleJoinRequest's doc comment
// for the full reasoning and the narrow race this ordering accepts.
//
// A false return (no error, not admitted) means a concurrent caller won
// the race to mark this identity first -- both callers had already issued
// a leaf by this point, so this is reported for logging, not as a failure
// requiring either leaf to be revoked.
func MarkJoinIdentityAdmitted(ctx context.Context, store kvstore.AtomicKVStore, nodeURI string) error {
	identityKey := joinIdentityPrefix + nodeURI
	if _, err := store.PutIfAbsent(ctx, identityKey, []byte(time.Now().UTC().Format(time.RFC3339))); err != nil {
		return &JoinTokenStoreError{Op: "putifabsent", Detail: identityKey, Err: err}
	}
	return nil
}

// errJoinTokenRejected is the one wire-visible reason every ConsumeJoinToken
// failure reports, regardless of cause. See ConsumeJoinToken's own doc
// comment on why the causes are not distinguished on the wire.
var errJoinTokenRejected = &JoinRefusedError{Reason: "join token rejected"}

// InvalidJoinIdentityError is returned by MintJoinToken when the supplied
// trust domain or node id cannot form a valid SPIFFE node identity.
type InvalidJoinIdentityError struct {
	Err error
}

// Error returns a human-readable description.
func (e *InvalidJoinIdentityError) Error() string {
	return "clusterenroll: invalid join identity: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *InvalidJoinIdentityError) Unwrap() error { return e.Err }

// JoinTokenTTLError is returned by MintJoinToken when ttl is outside
// [MinJoinTokenTTL, MaxJoinTokenTTL].
type JoinTokenTTLError struct {
	TTL time.Duration
}

// Error returns a human-readable description.
func (e *JoinTokenTTLError) Error() string {
	return "clusterenroll: join token TTL out of range"
}

// JoinTokenGenerateError wraps a failure generating or encoding a fresh
// join token.
type JoinTokenGenerateError struct {
	Err error
}

// Error returns a human-readable description.
func (e *JoinTokenGenerateError) Error() string {
	return "clusterenroll: join token generation failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *JoinTokenGenerateError) Unwrap() error { return e.Err }

// JoinTokenStoreError wraps a failure reading or writing the replicated
// join-token store.
type JoinTokenStoreError struct {
	Op     string
	Detail string
	Err    error
}

// Error returns a human-readable description.
func (e *JoinTokenStoreError) Error() string {
	return "clusterenroll: join token store " + e.Op + " failed (" + e.Detail + "): " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *JoinTokenStoreError) Unwrap() error { return e.Err }

// JoinTokenCollisionError is returned by MintJoinToken on the
// astronomically unlikely event that a freshly generated token's digest
// already exists in the store. Refused rather than silently retried or
// overwritten, exactly as cakey's PutIfAbsent-arbitrated generation refuses
// a losing race rather than guessing which candidate should win.
type JoinTokenCollisionError struct{}

// Error returns a human-readable description.
func (e *JoinTokenCollisionError) Error() string {
	return "clusterenroll: join token digest collision"
}

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
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
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
// #nosec G101 -- replicated-store key-space prefix, not a credential.
const joinTokenPrefix = "__sys:cluster_join_tokens/"

// joinIdentityPrefix records, per node identity, either that it has been
// ADMITTED (a durable RFC 3339 timestamp, written by
// PromoteJoinIdentityAdmitted or MarkJoinIdentityAdmitted) or that an
// issuance for it is PENDING (an expiring "pending:<unix-deadline>" record,
// written by ReserveJoinIdentity -- see pendingValue).
//
// Admitted entries are never removed: a SECOND join attempt presenting a
// (new, validly minted) token for an identity that has already joined is
// refused rather than silently re-admitted -- "refuse identities of
// existing members" -- because this package cannot tell "the first leaf
// never reached the joiner" apart from "an attacker is attempting to mint
// a second identity on top of an admitted one" from here; an operator
// re-admitting a genuinely lost node mints a token for a NEW node id,
// which is a deliberate, auditable action.
//
// PENDING entries are NOT permanent. A crash between ReserveJoinIdentity
// and PromoteJoinIdentityAdmitted/ReleaseJoinIdentityReservation, a failed
// Promote, or a Release that could not run (an expired request ctx, an
// unknown-outcome store error) all leave one behind -- and unlike an
// admitted record, there is no later admin action that un-wedges it: the
// node id would be refused forever by MintJoinToken/ConsumeJoinToken with
// no path to recovery. MintJoinToken, ConsumeJoinToken and
// ReserveJoinIdentity therefore all treat a PENDING record past its
// encoded deadline as absent, and ReserveJoinIdentity reclaims it with a
// CompareAndSwap from the exact stale value observed -- never a blind
// overwrite, so a concurrent reclaimer or a Promote that lands between the
// read and the swap is not clobbered.
const joinIdentityPrefix = "__sys:cluster_join_identities/"

// joinPendingValuePrefix marks a joinIdentityPrefix record as an
// in-progress reservation rather than an admitted timestamp. Distinct from
// any valid RFC 3339 timestamp (what an admitted record holds: that format
// never starts with a lowercase letter), so a reader can always tell the
// two states apart.
const joinPendingValuePrefix = "pending:"

// admittedValuePrefix marks a joinIdentityPrefix record as an ADMITTED
// JSON envelope (admittedRecord), written by every admission from this
// point forward. Earlier releases wrote the bare RFC 3339 timestamp
// parseAdmitted still accepts as a legacy shape (see its own doc comment);
// this prefix is how a reader tells the two apart without guessing from
// content alone.
const admittedValuePrefix = "admitted:"

// retiredValuePrefix marks a joinIdentityPrefix record as explicitly
// RETIRED: an admin decided (RetireJoinIdentity) that this node identity
// may never be (re-)admitted, whether or not its most recent leaf has
// lapsed. Unlike a PENDING or LAPSED admitted record, a retired one is
// never reclaimed and never expires.
const retiredValuePrefix = "retired:"

// admittedRecord is the JSON envelope an ADMITTED joinIdentityPrefix
// record holds, under admittedValuePrefix. NotAfter is the issued leaf's
// own expiry: once passed, classifyIdentityValue reports the identity as
// LAPSED rather than ACTIVE, and MintJoinToken/ConsumeJoinToken allow a
// fresh token to re-admit the SAME identity under the ordinary join rules
// -- "renewing" a lapsed admission, never a special-cased renewal path of
// its own. A zero NotAfter -- every record parseAdmitted decodes from the
// legacy bare-RFC3339 shape, where no expiry was ever recorded -- means
// "expiry unknown": fail closed, treated as perpetually active, never
// lapsing.
type admittedRecord struct {
	AdmittedAt time.Time `json:"admitted_at"`
	NotAfter   time.Time `json:"not_after"`

	// Serial is the issued leaf's certificate serial number (decimal), kept
	// for audit only -- a retirement can be logged against the exact
	// certificate it retires. Never used to look anything up.
	Serial string `json:"serial,omitempty"`
}

// retiredRecord is the JSON envelope a RETIRED joinIdentityPrefix record
// holds, under retiredValuePrefix.
type retiredRecord struct {
	RetiredAt time.Time `json:"retired_at"`
	Reason    string    `json:"reason,omitempty"`
}

// encodeAdmitted builds the value PromoteJoinIdentityAdmitted and
// MarkJoinIdentityAdmitted write for a newly (re-)admitted identity.
func encodeAdmitted(notAfter time.Time, serial string) []byte {
	// admittedRecord's fields are all JSON-safe (time.Time, string); this
	// cannot fail for any value this package constructs.
	data, _ := json.Marshal(admittedRecord{
		AdmittedAt: time.Now().UTC(),
		NotAfter:   notAfter.UTC(),
		Serial:     serial,
	})
	return append([]byte(admittedValuePrefix), data...)
}

// parseAdmitted reports the admittedRecord encoded in an ADMITTED-shaped
// value and whether value is actually admitted-shaped at all: true for the
// current admittedValuePrefix JSON envelope OR the legacy bare RFC 3339
// timestamp earlier releases wrote (decoded with a zero NotAfter, which
// never lapses -- see admittedRecord's own doc comment); false for a
// PENDING or RETIRED record, or anything this package did not write.
func parseAdmitted(value []byte) (admittedRecord, bool) {
	s := string(value)
	if rest, ok := strings.CutPrefix(s, admittedValuePrefix); ok {
		var rec admittedRecord
		if err := json.Unmarshal([]byte(rest), &rec); err != nil {
			return admittedRecord{}, false
		}
		return rec, true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return admittedRecord{AdmittedAt: t}, true
	}
	return admittedRecord{}, false
}

// encodeRetired builds the value RetireJoinIdentity writes.
func encodeRetired(reason string) []byte {
	// retiredRecord's fields are all JSON-safe; this cannot fail.
	data, _ := json.Marshal(retiredRecord{RetiredAt: time.Now().UTC(), Reason: reason})
	return append([]byte(retiredValuePrefix), data...)
}

// parseRetired reports whether value is a RETIRED-shaped record.
func parseRetired(value []byte) (retiredRecord, bool) {
	rest, ok := strings.CutPrefix(string(value), retiredValuePrefix)
	if !ok {
		return retiredRecord{}, false
	}
	var rec retiredRecord
	if err := json.Unmarshal([]byte(rest), &rec); err != nil {
		return retiredRecord{}, false
	}
	return rec, true
}

// identityState is what a joinIdentityPrefix record's current value means
// for a new MintJoinToken/ConsumeJoinToken/ReserveJoinIdentity call.
type identityState int

const (
	// identityAbsent means no live record blocks this identity: no key at
	// all, or a PENDING reservation whose TTL has lapsed (reclaimable).
	identityAbsent identityState = iota
	// identityPendingLive means an issuance for this identity is already
	// in flight, within its reservation TTL.
	identityPendingLive
	// identityActive means this identity holds a current admission: its
	// leaf has not lapsed (or its expiry is unknown -- fail closed).
	identityActive
	// identityLapsed means this identity was admitted, but its leaf's
	// NotAfter has passed: eligible to be renewed under the ordinary join
	// rules, exactly like identityAbsent, EXCEPT that ReserveJoinIdentity
	// must reclaim it via CompareAndSwap from its exact value rather than
	// PutIfAbsent (the key is not absent, it holds the lapsed record).
	identityLapsed
	// identityRetired means an admin has permanently refused this
	// identity (RetireJoinIdentity): never reclaimed, never lapses.
	identityRetired
)

// classifyIdentityValue classifies a raw joinIdentityPrefix record value.
// data == nil means the key does not exist (identityAbsent). An
// unrecognized non-nil shape -- a value from a build this package does not
// know how to parse -- is classified identityActive: the same fail-closed
// direction parseAdmitted's own legacy fallback takes, so a record this
// code cannot explain away never silently becomes available for reuse.
func classifyIdentityValue(data []byte) identityState {
	if data == nil {
		return identityAbsent
	}
	if _, ok := parseRetired(data); ok {
		return identityRetired
	}
	if deadline, ok := parsePendingDeadline(data); ok {
		if time.Now().After(deadline) {
			return identityAbsent
		}
		return identityPendingLive
	}
	rec, ok := parseAdmitted(data)
	if !ok {
		return identityActive
	}
	if rec.NotAfter.IsZero() || time.Now().Before(rec.NotAfter) {
		return identityActive
	}
	return identityLapsed
}

// identityBlocksAdmission reports whether state must refuse a new mint or
// consume for the identity it was read from: a live reservation, a current
// admission, or an explicit retirement. identityAbsent and identityLapsed
// do not block -- a lapsed admission is renewed under the ordinary join
// rules, not a special case.
func identityBlocksAdmission(state identityState) bool {
	switch state {
	case identityPendingLive, identityActive, identityRetired:
		return true
	default:
		return false
	}
}

// DefaultJoinIdentityReservationTTL is used when ReserveJoinIdentity is
// called with ttl <= 0. A few minutes is generous for the sign-and-promote
// sequence it bounds (ordinarily milliseconds) while still reclaiming a
// wedged reservation long before an operator would notice and investigate.
const DefaultJoinIdentityReservationTTL = 5 * time.Minute

// promoteReleaseTimeout bounds PromoteJoinIdentityAdmitted's and
// ReleaseJoinIdentityReservation's OWN context, independent of the ctx
// HandleJoinRequest calls them with. Both run to finalize an outcome
// (a successful sign, or a failed one) that has ALREADY happened by the
// time either is called, so a request ctx that is canceled or past its
// deadline at that exact moment -- the client gave up, or the request's
// own deadline passed while IssueLeafForKey was signing -- must not also
// abort the cleanup that keeps the node id from wedging; see
// context.WithoutCancel in both functions.
const promoteReleaseTimeout = 10 * time.Second

// pendingValue encodes a reservation's deadline as the record value
// ReserveJoinIdentity writes.
func pendingValue(deadline time.Time) []byte {
	return []byte(joinPendingValuePrefix + strconv.FormatInt(deadline.Unix(), 10))
}

// parsePendingDeadline reports the deadline encoded in a PENDING-shaped
// value and whether value is actually PENDING-shaped at all. ok is false
// for an admitted RFC 3339 timestamp, for a value this package did not
// write, or for a record from a build that predates this encoding --
// which decode as "not pending" and therefore as a blocking (admitted-
// shaped) record rather than as absent, the fail-closed direction for an
// unrecognised value.
func parsePendingDeadline(value []byte) (deadline time.Time, ok bool) {
	s := string(value)
	rest, isPending := strings.CutPrefix(s, joinPendingValuePrefix)
	if !isPending {
		return time.Time{}, false
	}
	secs, err := strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(secs, 0).UTC(), true
}

// classifyIdentity reads identityKey's current record and classifies it
// (see identityState): a live reservation, a current admission, a lapsed
// one eligible for renewal, a retirement, or absent. See
// identityBlocksAdmission for which of these refuse a new mint or consume.
func classifyIdentity(ctx context.Context, store kvstore.AtomicKVStore, identityKey string) (identityState, error) {
	data, err := store.Get(ctx, identityKey)
	if err != nil {
		return identityAbsent, &JoinTokenStoreError{Op: "get", Detail: identityKey, Err: err}
	}
	return classifyIdentityValue(data), nil
}

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
// Refuses with *ExistingMemberError when nodeID already has a record in
// the join-identity ledger (joinIdentityPrefix): the cluster CA has
// already issued (or, for a founder or first-boot node, recorded without
// re-issuing -- see cmd/qrdbd's recordOwnJoinIdentity) a certificate for
// this node identity, so minting a second token for it is never a correct
// admin action -- the identity is either already a member or already
// holds an unconsumed token from an earlier mint that was never for this
// check to second-guess.
//
// ttl <= 0 uses DefaultJoinTokenTTL; a ttl outside
// [MinJoinTokenTTL, MaxJoinTokenTTL] is refused with *JoinTokenTTLError.
func MintJoinToken(ctx context.Context, store kvstore.AtomicKVStore, trustDomain, nodeID string, ttl time.Duration) (token string, expiresAt time.Time, err error) {
	identity := spiffe.NodeIdentity{TrustDomain: trustDomain, NodeID: nodeID}
	if err := identity.Validate(); err != nil {
		return "", time.Time{}, &InvalidJoinIdentityError{Err: err}
	}

	identityKey := joinIdentityPrefix + identity.String()
	state, err := classifyIdentity(ctx, store, identityKey)
	if err != nil {
		return "", time.Time{}, err
	}
	if state == identityRetired {
		return "", time.Time{}, &RetiredJoinIdentityError{NodeURI: identity.String()}
	}
	if identityBlocksAdmission(state) {
		return "", time.Time{}, &ExistingMemberError{NodeURI: identity.String()}
	}

	// A negative ttl is refused outright, typed, rather than silently
	// treated as "use the default": it is never a value any caller meant
	// to send, and defaulting it would hide a caller bug (a bad
	// subtraction, an unconverted unit) behind an ordinary-looking token.
	// Only EXACTLY zero means "use the default" -- the documented contract
	// MintJoinToken and service.ParseJoinTokenTTLSeconds (ttlSeconds <= 0
	// -> 0 duration) both share.
	if ttl < 0 {
		return "", time.Time{}, &JoinTokenTTLError{TTL: ttl}
	}
	if ttl == 0 {
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
// This does NOT reserve or mark the identity admitted -- it only refuses a
// token bound to an identity ALREADY admitted or already reserved for an
// in-flight issuance (a read-only check against the same ledger
// ReserveJoinIdentity/PromoteJoinIdentityAdmitted write to). See
// HandleJoinRequest's doc comment for the full reserve-then-promote-or-
// release sequence that runs after this returns.
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
	state, err := classifyIdentity(ctx, store, identityKey)
	if err != nil {
		return "", err
	}
	if identityBlocksAdmission(state) {
		return "", errJoinTokenRejected
	}

	return identity.String(), nil
}

// MarkJoinIdentityAdmitted records that nodeURI has been admitted WITHOUT
// going through a reservation first, carrying notAfter and serial (the
// node's own certificate's expiry and serial, read back by the caller) so
// a later lapse can be detected the same way PromoteJoinIdentityAdmitted's
// own record is -- the founder/first-boot path
// (cmd/qrdbd's recordOwnJoinIdentity), where there is no concurrent-token
// race to protect against and no signing step that can fail after the
// write: the certificate this records already exists, generated directly,
// not issued against a presented token. The member-side CSR join path
// (HandleJoinRequest) does NOT use this -- it goes through
// ReserveJoinIdentity / PromoteJoinIdentityAdmitted /
// ReleaseJoinIdentityReservation instead, so that a signing failure
// between consuming a token and recording admission cannot either retire
// the node ID forever or leave two concurrent tokens for one identity both
// believing they won.
//
// Idempotent by design: called on every boot, not only the first, so a
// restart presenting the SAME leaf must not error just because the record
// already exists. A false return (no error, not admitted) therefore means
// exactly that -- reported at INFO rather than silently discarded, since
// it is also the shape a genuine split-brain (two different processes
// both believing they are THIS node) would take, which an operator
// watching logs should be able to notice even though this call does not
// fail on it.
func MarkJoinIdentityAdmitted(ctx context.Context, store kvstore.AtomicKVStore, nodeURI string, notAfter time.Time, serial string) error {
	identityKey := joinIdentityPrefix + nodeURI
	stored, err := store.PutIfAbsent(ctx, identityKey, encodeAdmitted(notAfter, serial))
	if err != nil {
		return &JoinTokenStoreError{Op: "putifabsent", Detail: identityKey, Err: err}
	}
	if !stored {
		slog.InfoContext(ctx, "member-side CSR join: identity already recorded admitted",
			"node_uri", nodeURI)
	}
	return nil
}

// ReserveJoinIdentity atomically claims nodeURI in the join-identity
// ledger for an issuance about to be attempted, BEFORE IssueLeafForKey
// ever runs. It shares the same ledger key MintJoinToken's own
// already-admitted check reads, so a second, independently-and-validly
// minted token for the SAME identity -- two concurrent join attempts
// racing each other -- is refused HERE, at the reservation, rather than
// both reaching IssueLeafForKey and each believing it will win.
//
// ttl <= 0 uses DefaultJoinIdentityReservationTTL. The returned pending
// value MUST be passed to the matching PromoteJoinIdentityAdmitted or
// ReleaseJoinIdentityReservation call: it is the exact record this
// reservation wrote, which is what those calls compare against, not a
// fixed sentinel, because the deadline it encodes is unique to this
// attempt.
//
// reserved=false (no error) means nodeURI is already reserved (within its
// TTL) or already admitted; the caller must refuse the join without ever
// calling IssueLeafForKey. A reservation whose TTL has LAPSED is reclaimed
// here instead of left to block forever: this call reads the stale value
// and retries as a CompareAndSwap from exactly that value, never a blind
// overwrite, so a concurrent reclaimer (or a Promote that lands between
// the read and the swap) is not clobbered.
func ReserveJoinIdentity(ctx context.Context, store kvstore.AtomicKVStore, nodeURI string, ttl time.Duration) (reserved bool, pending []byte, err error) {
	if ttl <= 0 {
		ttl = DefaultJoinIdentityReservationTTL
	}
	identityKey := joinIdentityPrefix + nodeURI
	fresh := pendingValue(time.Now().Add(ttl))

	stored, err := store.PutIfAbsent(ctx, identityKey, fresh)
	if err != nil {
		return false, nil, &JoinTokenStoreError{Op: "putifabsent", Detail: identityKey, Err: err}
	}
	if stored {
		return true, fresh, nil
	}

	existing, err := store.Get(ctx, identityKey)
	if err != nil {
		return false, nil, &JoinTokenStoreError{Op: "get", Detail: identityKey, Err: err}
	}
	// A nil existing read means the key vanished between the failed
	// PutIfAbsent above and this Get (an extremely rare race): not ours to
	// take via CompareAndSwap from a value that is no longer there to
	// expect -- the caller simply loses this attempt, exactly as before
	// this function learned about lapsed admissions.
	if existing == nil {
		return false, nil, nil
	}
	// Reclaimable here: a PENDING reservation past its TTL (classifies
	// identityAbsent, since classifyIdentityValue cannot tell "no key" from
	// "an expired reservation" apart by design -- both are safe to take
	// over), or an ADMITTED record whose leaf has LAPSED. Not reclaimable:
	// a still-live reservation, an active admission, or a retirement.
	// Reclaimed via CompareAndSwap from EXACTLY the value just read, never
	// a blind overwrite, so a concurrent reclaimer (or a Promote/Retire
	// that lands between the read and the swap) is not clobbered.
	switch classifyIdentityValue(existing) {
	case identityAbsent, identityLapsed:
	default:
		return false, nil, nil
	}
	swapped, err := store.CompareAndSwap(ctx, identityKey, existing, fresh)
	if err != nil {
		return false, nil, &JoinTokenStoreError{Op: "compareandswap", Detail: identityKey, Err: err}
	}
	if !swapped {
		// Lost the reclaim race to a concurrent reserver (or a
		// Promote/Retire that landed in between); the identity is spoken
		// for either way.
		return false, nil, nil
	}
	return true, fresh, nil
}

// PromoteJoinIdentityAdmitted swaps nodeURI's PENDING reservation
// (ReserveJoinIdentity, which returned pending) for a durable admitted
// record carrying notAfter and serial -- the issued leaf's own expiry and
// certificate serial, read back from the certificate IssueLeafForKey just
// signed -- so a LATER join attempt for the SAME identity can tell an
// active admission from a lapsed one (see identityState) without a second
// signing-time lookup. Called ONLY after IssueLeafForKey has actually
// signed a leaf for it. The caller MUST refuse the whole issue -- never
// return the leaf it
// already signed -- when this returns an error: a non-nil error means the
// swap's outcome is unknown, and swapped=false means the reservation was
// not there in the exact PENDING state this call expected (it expired and
// was reclaimed, or was reclaimed concurrently), an inconsistency this
// package cannot explain away well enough to hand out a certificate
// anyway.
//
// Runs under its own timeout on a DETACHED context
// (context.WithoutCancel(ctx)): this call finalizes a signing success
// that has ALREADY happened, so the caller's ctx being canceled or past
// its deadline at this exact moment must not also abort the write that
// keeps the ledger consistent with the certificate already issued.
//
// A CompareAndSwap error means the swap's OUTCOME is unknown, not that it
// failed: a timeout after the write actually committed looks identical
// from here to one before it did. Treating every error as "did not
// happen" is itself a wedge -- the record is admitted forever either way,
// but the caller, told it failed, refuses to return the leaf it already
// signed, so the node that should hold it never does, and ConsumeJoinToken
// already spent the one-use token getting here. On an error, this
// re-reads the key: if it now holds exactly the admitted value this call
// just attempted, the swap DID commit despite the error, and it is safe
// to report success so the caller returns the leaf. A re-read that fails,
// or finds anything else, leaves the original error as the honest answer
// -- the outcome is still genuinely unknown.
func PromoteJoinIdentityAdmitted(ctx context.Context, store kvstore.AtomicKVStore, nodeURI string, pending []byte, notAfter time.Time, serial string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), promoteReleaseTimeout)
	defer cancel()
	identityKey := joinIdentityPrefix + nodeURI
	admitted := encodeAdmitted(notAfter, serial)
	swapped, err := store.CompareAndSwap(ctx, identityKey, pending, admitted)
	if err != nil {
		if current, getErr := store.Get(ctx, identityKey); getErr == nil && bytes.Equal(current, admitted) {
			return nil
		}
		return &JoinTokenStoreError{Op: "compareandswap", Detail: identityKey, Err: err}
	}
	if !swapped {
		return &JoinIdentityReservationLostError{NodeURI: nodeURI}
	}
	return nil
}

// ReleaseJoinIdentityReservation removes nodeURI's PENDING reservation
// (ReserveJoinIdentity, which returned pending), called when issuance
// fails AFTER the reservation succeeded: the identity was never actually
// admitted, so the reservation must not survive forever and block every
// later join attempt for it.
//
// Runs under its own timeout on a DETACHED context
// (context.WithoutCancel(ctx)), for the identical reason
// PromoteJoinIdentityAdmitted does: this call finalizes a signing FAILURE
// that has already happened, and the caller's ctx being canceled or past
// its deadline must not also abort the cleanup. Even if it does run and
// still fails -- or the request process crashes before it runs at all --
// the reservation is not wedged forever: it carries the same deadline
// ReserveJoinIdentity encoded, and a later ReserveJoinIdentity for the
// same identity reclaims it once that deadline passes.
//
// released=false (no error) means the reservation was not there, or no
// longer held the exact PENDING value this call expected -- reported at
// WARN, the lost-race case the earlier, discarding implementation of this
// cleanup did not surface at all, rather than silently treated as
// "nothing to do".
func ReleaseJoinIdentityReservation(ctx context.Context, store kvstore.AtomicKVStore, nodeURI string, pending []byte) (released bool, err error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), promoteReleaseTimeout)
	defer cancel()
	identityKey := joinIdentityPrefix + nodeURI
	released, err = store.CompareAndDelete(ctx, identityKey, pending)
	if err != nil {
		return false, &JoinTokenStoreError{Op: "compareanddelete", Detail: identityKey, Err: err}
	}
	if !released {
		slog.WarnContext(ctx, "member-side CSR join: reservation already gone when releasing it after a signing failure",
			"node_uri", nodeURI)
	}
	return released, nil
}

// RetireJoinIdentity permanently refuses any FUTURE admission for
// spiffe://trustDomain/node/nodeID -- a fresh MintJoinToken, or a renewal
// of a LAPSED one (see identityState) -- after an admin has decided this
// identity must never (re-)join: a member removed from the cluster, or
// one whose issued leaf must be treated as revoked. Unlike an
// admitted-but-lapsed record, a RETIRED one is never reclaimed by
// ReserveJoinIdentity and never expires on its own. reason is an optional,
// free-text operator note carried for audit only -- never parsed or
// compared.
//
// Retiring is unconditional (store.Put, not a CompareAndSwap from an
// expected prior value): it must succeed over every prior state --
// absent (pre-emptively ban an identity that has never attempted to join),
// a live reservation, an active or lapsed admission, or an already-retired
// record (idempotent) -- because an admin retiring an identity is a rare,
// deliberate action that must not be refused just because this package
// cannot predict what state it finds. This accepts one documented race: a
// join already past ReserveJoinIdentity when the retirement lands can
// still complete -- IssueLeafForKey already has the token's single use
// spent -- and PromoteJoinIdentityAdmitted's own CompareAndSwap then
// overwrites the retirement with a fresh ADMITTED record. An operator who
// needs retirement to win a race already in flight retires the identity
// again afterward; this package does not attempt to serialize against an
// issuance already in progress.
func RetireJoinIdentity(ctx context.Context, store kvstore.AtomicKVStore, trustDomain, nodeID, reason string) error {
	identity := spiffe.NodeIdentity{TrustDomain: trustDomain, NodeID: nodeID}
	if err := identity.Validate(); err != nil {
		return &InvalidJoinIdentityError{Err: err}
	}
	identityKey := joinIdentityPrefix + identity.String()
	if err := store.Put(ctx, identityKey, encodeRetired(reason)); err != nil {
		return &JoinTokenStoreError{Op: "put", Detail: identityKey, Err: err}
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

// JoinIdentityReservationLostError is returned by
// PromoteJoinIdentityAdmitted when NodeURI's PENDING reservation was not
// there in the state the promotion expected -- HandleJoinRequest's
// signal to refuse the issue it already signed rather than hand it out.
type JoinIdentityReservationLostError struct {
	NodeURI string
}

// Error returns a human-readable description.
func (e *JoinIdentityReservationLostError) Error() string {
	return "clusterenroll: join identity reservation lost before it could be promoted to admitted: " + e.NodeURI
}

// ExistingMemberError is returned by MintJoinToken when NodeURI already has
// a record in the join-identity ledger: the cluster CA has already issued
// (or a founder/first-boot node has had recorded, without re-issuing) a
// certificate for this node identity. Minting a token for an identity
// already in the cluster is always wrong, whether that identity is live
// right now or holds an unconsumed token from an earlier mint.
type ExistingMemberError struct {
	NodeURI string
}

// Error returns a human-readable description.
func (e *ExistingMemberError) Error() string {
	return "clusterenroll: node identity already admitted to the cluster: " + e.NodeURI
}

// RetiredJoinIdentityError is returned by MintJoinToken when NodeURI has
// been explicitly retired (RetireJoinIdentity): an admin decided this
// identity may never be (re-)admitted, regardless of whether its most
// recent leaf has since lapsed. Distinct from ExistingMemberError, which
// names an identity still actively a member or with a reservation in
// flight -- both eligible to retry once lapsed, which a retired identity
// never is.
type RetiredJoinIdentityError struct {
	NodeURI string
}

// Error returns a human-readable description.
func (e *RetiredJoinIdentityError) Error() string {
	return "clusterenroll: node identity has been retired and may never be (re-)admitted: " + e.NodeURI
}

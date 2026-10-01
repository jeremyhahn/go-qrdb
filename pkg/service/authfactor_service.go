// Copyright 2026 Jeremy Hahn
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

package service

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
)

// sysEnrollmentPrefix is the KV namespace for persisted enrollment records.
const sysEnrollmentPrefix = "__sys:authfactor/"

// sysChallengePrefix is the KV namespace for pending challenges. It is
// deliberately a sibling of sysEnrollmentPrefix rather than a child of it
// ("...factor:challenge/", not "...factor/challenge/"): ListFactors lists
// sysEnrollmentPrefix, and a challenge record living inside that namespace
// would have to be filtered back out by type rather than excluded by key
// shape. Beneath it, records are grouped by expiry bucket (see
// challengeKey), which is what lets the expiry sweep read only expired
// records.
//
// Challenges are persisted here, not cached in this service's memory,
// specifically so that no operation on this type depends on which
// *authFactorService instance answers it — see the package doc on
// routingAuthFactorService in authfactor_routing.go for why that dependency
// is the defect a caching scheme cannot fix from the routing side alone.
const sysChallengePrefix = "__sys:authfactor:challenge/"

// AuthFactorListReadError is returned when ListFactors cannot read one of
// the enrollment records its own List call named, for a reason other than
// the record having been concurrently deleted (see readRecord). It aborts
// the call rather than silently omitting the record from the result: a
// sealed barrier between the List and this Get is the case that matters
// most — the caller must see the same failure a direct read of that record
// would give, not a shorter-than-true list reported as complete.
type AuthFactorListReadError struct {
	Key string
	Err error
}

// Error returns a human-readable description of the read failure.
func (e *AuthFactorListReadError) Error() string {
	return "service: list factors: read " + e.Key + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *AuthFactorListReadError) Unwrap() error {
	return e.Err
}

// ErrAuthFactorAtomicStoreRequired is returned by VerifyFactor when the
// configured KV store cannot perform an atomic conditional delete.
//
// Single-use challenge consumption needs one: a Get followed by an
// unconditional Put lets two concurrent VerifyFactor calls both read the same
// pending challenge before either clears it, so both invoke Factor.Verify and
// the challenge is replayed (go-qrdb#464 round 4). Every store this service
// is actually built over supplies it — KVServicer declares CompareAndDelete
// as a required method, and the per-tenant kvstore.KVStore a
// tenant.StoreResolver returns is documented to implement
// kvstore.AtomicKVStore — so this error signals a test double or a future
// store that does not, and consumeChallenge refuses to proceed non-atomically
// rather than let the exactly-once guarantee quietly not hold.
var ErrAuthFactorAtomicStoreRequired = errors.New("service: kv store does not support atomic conditional delete; challenge consumption cannot be guaranteed exactly-once")

// atomicChallengeStore is the one verb consumeChallenge needs beyond
// AuthFactorKV's three. It is declared locally, narrowed to that single
// method, so it is discovered by type assertion exactly as
// kvstore.AtomicKVStore is discovered elsewhere in this repository: a store
// offering only Put/Get/List is found NOT to support it, rather than the two
// interfaces silently disagreeing about method sets.
type atomicChallengeStore interface {
	// CompareAndDelete atomically removes key only when its stored value
	// still equals expected, reporting whether this call removed it. Both
	// KVServicer and kvstore.AtomicKVStore declare this exact signature.
	CompareAndDelete(ctx context.Context, key string, expected []byte) (deleted bool, err error)
}

// AuthFactorServicer orchestrates enrollment, challenge, and verification
// across all registered factor types (PKCS#11, PIV, FIDO2) for the
// ceremony-time custodian surface (docs/auth-factors/ calls this
// Client.AuthFactors(), as opposed to the user-side UserAuthFactorServicer).
// Enrollment records are persisted under __sys:authfactor/<id> via the KV
// service. No secret key material is ever persisted.
//
// An implementation built by NewAuthFactorService holds ONE tenant's store.
// A composition serving more than one tenant must build
// NewRoutingAuthFactorService instead, which resolves each call's store from
// the context tenant (go-qrdb#464): a subject and an enrollment id carry no
// tenant of their own, so nothing but the resolved store separates one
// tenant's custodian enrolments from another's.
type AuthFactorServicer interface {
	// EnrollFactor registers a user with a named factor.
	// method must be one of "pkcs11", "piv", "fido2".
	// Returns an Enrollment containing the public credential and metadata.
	EnrollFactor(ctx context.Context, factorName string, subject string, params authfactors.EnrollParams) (*authfactors.Enrollment, error)

	// ChallengeFactor issues a nonce-based challenge for the enrollment identified
	// by enrollmentID. The caller must submit a Response within the challenge TTL.
	// The returned ChallengeID is service-assigned ("<expiry bucket>.<factor
	// challenge id>") and opaque to the caller.
	ChallengeFactor(ctx context.Context, enrollmentID string) (*authfactors.Challenge, error)

	// VerifyFactor verifies the custodian's response against the stored
	// challenge, consuming it. challengeID must be the identifier
	// ChallengeFactor returned; any other shape is refused as
	// *authfactors.EnrollmentNotFoundError.
	//
	// The challenge is verified by the factor its enrollment was made with,
	// never by one the caller names. factorName may be empty; a non-empty
	// factorName that differs from the enrollment's factor is refused with
	// *authfactors.FactorMismatchError. A challenge whose enrollment has been
	// revoked since it was issued is refused with
	// *authfactors.EnrollmentRevokedError. The challenge is consumed in
	// every case.
	VerifyFactor(ctx context.Context, factorName string, challengeID string, response *authfactors.Response) error

	// ListFactors returns all active (non-revoked) enrollments for the given subject.
	// An empty subject returns all enrollments across all subjects.
	ListFactors(ctx context.Context, subject string) ([]*authfactors.Enrollment, error)

	// RevokeFactor marks the enrollment identified by enrollmentID as revoked.
	// Revoked enrollments cannot issue new challenges.
	RevokeFactor(ctx context.Context, enrollmentID string) error
}

// authFactorService is the default implementation of AuthFactorServicer.
//
// It holds NO in-process state beyond its (immutable, post-construction)
// factor registry: enrollments and pending challenges both live in kv. That
// is deliberate, not incidental — see the sysChallengePrefix doc — and it is
// what makes an instance of this type safe to construct fresh per call, or
// cache under any keying scheme, with no correctness dependency on two
// calls landing on the SAME instance.
type authFactorService struct {
	factors map[string]authfactors.Factor // keyed by factor.Name()
	kv      AuthFactorKV

	// now is the clock challenge bucketing and the expiry sweep read. It is
	// time.Now in production; tests substitute a fixed or advancing clock so
	// bucket boundaries are deterministic.
	now func() time.Time
}

// AuthFactorServiceConfig holds the dependencies for NewAuthFactorService.
type AuthFactorServiceConfig struct {
	// Factors is the list of factor implementations to register.
	// Each factor is keyed by its Name() return value.
	Factors []authfactors.Factor

	// KV persists enrollment and pending-challenge records. It is ONE
	// tenant's store: a composition serving more than one tenant builds the
	// routed service (NewRoutingAuthFactorService) instead of passing a
	// shared store here.
	//
	// The type is AuthFactorKV rather than KVServicer — the same three-verb
	// narrowing UserAuthFactorServiceConfig uses — because it must be
	// constructible over EITHER store this repository hands out: the
	// replicated KVServicer a composition root holds directly, and the
	// per-tenant kvstore.KVStore a tenant.StoreResolver returns. Both
	// satisfy Put/Get/List verbatim, which is what lets the routed service
	// hand a resolved per-tenant store straight to this constructor with no
	// adapter between them.
	KV AuthFactorKV
}

// NewAuthFactorService constructs an authFactorService from the provided config.
// The service is safe for concurrent use.
func NewAuthFactorService(cfg AuthFactorServiceConfig) (AuthFactorServicer, error) {
	factors := make(map[string]authfactors.Factor, len(cfg.Factors))
	for _, f := range cfg.Factors {
		if f == nil {
			return nil, &authfactors.FactorRegisterError{Reason: "nil factor"}
		}
		key := factorKey(f.Name())
		// Two factors whose names normalise alike are one factor name: the
		// second would silently replace the first, and which one a challenge
		// is verified by would depend on list order.
		if _, dup := factors[key]; dup {
			return nil, &authfactors.FactorAlreadyRegisteredError{Name: key}
		}
		factors[key] = f
	}
	return &authFactorService{
		factors: factors,
		kv:      cfg.KV,
		now:     time.Now,
	}, nil
}

// EnrollFactor registers a user with the named factor and persists the
// resulting enrollment under __sys:authfactor/<id>.
func (s *authFactorService) EnrollFactor(ctx context.Context, factorName string, subject string, params authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	f, err := s.factor(factorName)
	if err != nil {
		return nil, err
	}

	params.UserID = subject

	enrollment, err := f.Enroll(ctx, &params)
	if err != nil {
		return nil, err
	}

	if err := s.persistEnrollment(ctx, enrollment); err != nil {
		return nil, err
	}

	return enrollment, nil
}

// ChallengeFactor loads the enrollment from the KV store and issues a
// challenge via the appropriate factor. The challenge is persisted to the KV
// store — the same one the enrollment lives in — until Verify consumes it or
// it expires unclaimed.
func (s *authFactorService) ChallengeFactor(ctx context.Context, enrollmentID string) (*authfactors.Challenge, error) {
	enrollment, err := s.loadEnrollment(ctx, enrollmentID)
	if err != nil {
		return nil, err
	}

	if !enrollment.RevokedAt.IsZero() {
		return nil, &authfactors.EnrollmentRevokedError{EnrollmentID: enrollmentID}
	}

	f, err := s.factor(enrollment.FactorName)
	if err != nil {
		return nil, err
	}

	// Amortized reclamation of unclaimed, expired challenges — see
	// sweepExpiredChallenges. Its cost is bounded by constants and never
	// touches a challenge that has not expired: it reads only expiry buckets
	// strictly older than now, so how many challenges are merely OUTSTANDING
	// has no effect on this call's store operations.
	//
	// Runs BEFORE the new challenge below is issued and persisted, and a new
	// challenge is never bucketed earlier than the current bucket (see
	// challengeBucket), so it can never reap the challenge this very call is
	// about to hand back.
	s.sweepExpiredChallenges(ctx)

	challenge, err := f.Challenge(ctx, enrollment)
	if err != nil {
		return nil, err
	}

	challengeID, err := s.persistChallenge(ctx, challenge)
	if err != nil {
		return nil, err
	}

	// The caller receives the service-assigned identifier, which names the
	// record's storage location (see persistChallenge); the stored record
	// keeps the factor's own identifier, which is what Factor.Verify sees.
	issued := *challenge
	issued.ChallengeID = challengeID
	return &issued, nil
}

// VerifyFactor consumes the pending challenge named by challengeID, loads the
// enrollment it was issued for, and calls Verify on the factor that
// enrollment was made with. See AuthFactorServicer.VerifyFactor.
func (s *authFactorService) VerifyFactor(ctx context.Context, factorName string, challengeID string, response *authfactors.Response) error {
	challenge, err := s.consumeChallenge(ctx, challengeID)
	if err != nil {
		return err
	}

	if challenge.Expired() {
		return &authfactors.ChallengeExpiredError{ChallengeID: challengeID}
	}

	// The enrollment's recorded factor is authoritative (go-qrdb#577). The
	// caller's factorName is at most an assertion about it: empty asserts
	// nothing, and anything else must agree or the request is refused. It is
	// never used to choose the factor, because a caller that could choose
	// could answer a strong factor's challenge through a weaker one.
	//
	// The enrollment is loaded unconditionally, which also means a challenge
	// issued before its enrollment was revoked can no longer be redeemed
	// after: ChallengeFactor refuses a revoked enrollment, and so does this.
	enrollment, err := s.loadEnrollment(ctx, challenge.EnrollmentID)
	if err != nil {
		return err
	}
	if !enrollment.RevokedAt.IsZero() {
		return &authfactors.EnrollmentRevokedError{EnrollmentID: enrollment.EnrollmentID}
	}
	if factorName != "" && factorKey(factorName) != factorKey(enrollment.FactorName) {
		return &authfactors.FactorMismatchError{
			EnrollmentID: enrollment.EnrollmentID,
			Enrolled:     enrollment.FactorName,
			Requested:    factorName,
		}
	}

	f, err := s.factor(enrollment.FactorName)
	if err != nil {
		return err
	}

	return f.Verify(ctx, challenge, response)
}

// ListFactors returns all persisted enrollment records for the given subject.
// An empty subject returns all enrollments.
//
// A failure reading any single record ABORTS the call and returns the
// failure rather than omitting that record from the result: a caller must
// never be told a shorter-than-true list is complete. This matters most for
// the condition it used to hide — a tenant's barrier sealing between the
// List call above and one of the Get calls below, which must surface as the
// same ErrTenantSealed a direct read would give, not as a quietly incomplete
// list.
func (s *authFactorService) ListFactors(ctx context.Context, subject string) ([]*authfactors.Enrollment, error) {
	if s.kv == nil {
		return nil, nil
	}

	keys, err := s.kv.List(ctx, sysEnrollmentPrefix)
	if err != nil {
		return nil, err
	}

	enrollments := make([]*authfactors.Enrollment, 0, len(keys))
	for _, key := range keys {
		data, err := s.readRecord(ctx, key)
		if err != nil {
			return nil, &AuthFactorListReadError{Key: key, Err: err}
		}
		if len(data) == 0 {
			// Raced with a concurrent revoke/delete between List and Get:
			// the record is genuinely gone now, which is not the partial-
			// success case above — there is nothing left to report on it.
			continue
		}
		var e authfactors.Enrollment
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, &authfactors.EnrollmentInvalidError{
				EnrollmentID: strings.TrimPrefix(key, sysEnrollmentPrefix),
				Reason:       "unmarshal failed: " + err.Error(),
			}
		}
		if subject != "" && e.UserID != subject {
			continue
		}
		enrollments = append(enrollments, &e)
	}

	return enrollments, nil
}

// RevokeFactor marks the enrollment as revoked (sets RevokedAt to now) and
// updates the persisted record.
func (s *authFactorService) RevokeFactor(ctx context.Context, enrollmentID string) error {
	enrollment, err := s.loadEnrollment(ctx, enrollmentID)
	if err != nil {
		return err
	}

	if !enrollment.RevokedAt.IsZero() {
		return &authfactors.EnrollmentRevokedError{EnrollmentID: enrollmentID}
	}

	enrollment.RevokedAt = time.Now().UTC()
	return s.persistEnrollment(ctx, enrollment)
}

// factor returns the registered factor for the given name or ErrFactorNotFound.
func (s *authFactorService) factor(name string) (authfactors.Factor, error) {
	f, ok := s.factors[factorKey(name)]
	if !ok {
		return nil, &authfactors.FactorNotFoundError{Name: name}
	}
	return f, nil
}

// factorKey is the ONE normalisation of a factor name: the registry lookup in
// factor and the enrollment-versus-caller comparison in VerifyFactor both use
// it, so two names that select the same factor can never be judged different
// factors, nor two that select different factors the same one.
func factorKey(name string) string { return authfactors.NormalizeName(name) }

// persistEnrollment serialises the enrollment to JSON and writes it to the KV store.
func (s *authFactorService) persistEnrollment(ctx context.Context, e *authfactors.Enrollment) error {
	if s.kv == nil {
		return nil
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return s.kv.Put(ctx, sysEnrollmentPrefix+e.EnrollmentID, data)
}

// readRecord reads key from the KV store, reporting a genuinely absent key
// as (nil, nil) regardless of which of AuthFactorKV's two conventions this
// store uses — a typed not-found error (KVServicer) or an empty value
// (kvstore.KVStore) — and propagating any OTHER error UNCHANGED. Folding
// every error into "absent" is exactly the bug this exists to avoid: a
// sealed barrier, a proposal failure, or any other operational error must
// reach the caller as itself, not as a false not-found.
func (s *authFactorService) readRecord(ctx context.Context, key string) ([]byte, error) {
	data, err := s.kv.Get(ctx, key)
	if err != nil {
		var knf *KeyNotFoundError
		if errors.As(err, &knf) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

// loadEnrollment reads an enrollment from the KV store by ID.
func (s *authFactorService) loadEnrollment(ctx context.Context, enrollmentID string) (*authfactors.Enrollment, error) {
	if s.kv == nil {
		return nil, &authfactors.EnrollmentNotFoundError{EnrollmentID: enrollmentID}
	}
	data, err := s.readRecord(ctx, sysEnrollmentPrefix+enrollmentID)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, &authfactors.EnrollmentNotFoundError{EnrollmentID: enrollmentID}
	}
	var e authfactors.Enrollment
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, &authfactors.EnrollmentInvalidError{
			EnrollmentID: enrollmentID,
			Reason:       "unmarshal failed: " + err.Error(),
		}
	}
	return &e, nil
}

// persistChallenge serialises the challenge to JSON, writes it to the KV
// store under its expiry bucket, and returns the challenge identifier the
// caller must present to VerifyFactor.
//
// The record lives at challengeKey(bucket, c.ChallengeID) and the returned
// identifier is "<bucket>.<c.ChallengeID>": the bucket travels inside the
// identifier so that consumeChallenge derives the exact key from it with no
// lookup, and the factor's own identifier is kept verbatim inside the
// record, so Factor.Verify receives the challenge exactly as the factor
// issued it.
//
// This is also the JSON boundary authfactors.Challenge.MethodData's doc
// comment requires values to survive: consumeChallenge below JSON-unmarshals
// what this call marshals. Every MethodData value is checked against that doc
// comment's contract here, at the one call site every challenge -- from any
// factor, over any transport -- passes through: a factor that supplies a
// non-string value is refused rather than allowed to ship a value that would
// arrive over gRPC and Unix as a stringified copy of itself while every other
// transport carried the native type, a six-transport contract violation this
// catches at the write, not the wire.
func (s *authFactorService) persistChallenge(ctx context.Context, c *authfactors.Challenge) (string, error) {
	if c.ChallengeID == "" {
		return "", &authfactors.EnrollmentInvalidError{
			EnrollmentID: c.EnrollmentID,
			Reason:       "factor issued a challenge with an empty ChallengeID",
		}
	}
	if key, ok := firstNonStringMethodDataKey(c.MethodData); ok {
		return "", &authfactors.EnrollmentInvalidError{
			EnrollmentID: c.EnrollmentID,
			Reason: "MethodData[\"" + key + "\"] is not a string; " +
				"pkg/api/grpc/proto's ChallengeFactorResponse.method_data is " +
				"map<string, string>, so every value must already be one",
		}
	}
	bucket := challengeBucket(c.ExpiresAt, s.now())
	if s.kv == nil {
		return challengePublicID(bucket, c.ChallengeID), nil
	}
	data, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	if err := s.kv.Put(ctx, challengeKey(bucket, c.ChallengeID), data); err != nil {
		return "", err
	}
	return challengePublicID(bucket, c.ChallengeID), nil
}

// firstNonStringMethodDataKey returns the first key (in an unspecified
// order -- map iteration order -- since the caller only needs A concrete
// offender to name in its error, not all of them) whose MethodData value is
// not a string, and reports whether one was found.
func firstNonStringMethodDataKey(md map[string]any) (string, bool) {
	for k, v := range md {
		if _, ok := v.(string); !ok {
			return k, true
		}
	}
	return "", false
}

// consumeChallenge reads a pending challenge and atomically deletes it from
// the KV store, so that of any number of concurrent VerifyFactor calls for
// the same challengeID, exactly one observes the challenge and every other
// is refused as not-found.
//
// challengeID is the identifier persistChallenge returned. It names its own
// storage key (see parseChallengeID); an identifier that does not have that
// shape cannot name any record this service wrote and is refused as
// not-found before the store is touched.
//
// A Get followed by an unconditional Put is NOT sufficient: two concurrent
// calls can both read the same pending challenge in the window before either
// clears it, and both would then invoke Factor.Verify, replaying the
// challenge (go-qrdb#464 round 4). The removal is instead a
// CompareAndDelete(key, data) guarded on the EXACT bytes just read: of two
// callers racing with the same expected value, the store's atomicity
// guarantees only one CompareAndDelete observes the key still holding it —
// the other finds it already gone and reports deleted=false — exactly the
// same guard PutIfAbsent's callers rely on for the create side of a
// single-use token (see KVServicer.PutIfAbsent and kvstore.AtomicKVStore).
//
// A store that cannot perform this atomically fails the call with
// ErrAuthFactorAtomicStoreRequired rather than degrade to a racy Get+Put: a
// checked refusal is preferable to a replay window that only manifests under
// load.
func (s *authFactorService) consumeChallenge(ctx context.Context, challengeID string) (*authfactors.Challenge, error) {
	if s.kv == nil {
		return nil, &authfactors.EnrollmentNotFoundError{EnrollmentID: challengeID}
	}
	atomicKV, ok := s.kv.(atomicChallengeStore)
	if !ok {
		return nil, ErrAuthFactorAtomicStoreRequired
	}
	key, ok := parseChallengeID(challengeID)
	if !ok {
		return nil, &authfactors.EnrollmentNotFoundError{EnrollmentID: challengeID}
	}
	data, err := s.readRecord(ctx, key)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, &authfactors.EnrollmentNotFoundError{EnrollmentID: challengeID}
	}
	deleted, err := atomicKV.CompareAndDelete(ctx, key, data)
	if err != nil {
		return nil, err
	}
	if !deleted {
		// Another VerifyFactor call (or the expiry sweep below) already
		// consumed this exact record between our Get and this
		// CompareAndDelete: refuse the replay rather than let a second
		// caller also succeed.
		return nil, &authfactors.EnrollmentNotFoundError{EnrollmentID: challengeID}
	}
	var c authfactors.Challenge
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, &authfactors.EnrollmentInvalidError{
			EnrollmentID: challengeID,
			Reason:       "unmarshal failed: " + err.Error(),
		}
	}
	return &c, nil
}

// Challenge expiry buckets.
//
// Every pending challenge is stored at
//
//	__sys:authfactor:challenge/<bucket>/<factor challenge id>
//
// where <bucket> is the challenge's expiry time, in whole
// authFactorChallengeBucketWidth units since the Unix epoch, written as a
// fixed-width zero-padded decimal (authFactorChallengeBucketDigits). Fixed
// width makes lexicographic key order equal numeric bucket order, so a key
// prefix names a contiguous, aligned RANGE of buckets: the full 12 digits
// name one bucket, the leading 11 name ten, the leading 10 name a hundred,
// and so on. The sweep relies on both properties.
const (
	// authFactorChallengeBucketWidth is the granularity expiry is truncated
	// to. A record is reclaimable no sooner than one full width (plus the
	// grace below) after it expires.
	authFactorChallengeBucketWidth = time.Minute

	// authFactorChallengeBucketDigits is the fixed decimal width of a bucket
	// number. Twelve digits of minutes reach far beyond any representable
	// deadline this service will be asked to store.
	authFactorChallengeBucketDigits = 12

	// authFactorChallengeSweepGraceBuckets is how many whole buckets behind
	// the current one the sweep stays. A bucket b is swept only when
	// b < bucket(now) - grace, so every record in it expired at least
	// grace*width ago by this node's clock. That margin absorbs clock skew
	// between the node that bucketed a record and the node that sweeps it:
	// a skewed sweeper cannot pass a bucket another node is still writing
	// into.
	authFactorChallengeSweepGraceBuckets = 1

	// authFactorChallengeSweepLists bounds how many List calls one sweep
	// makes. Each List covers one aligned block of already-expired buckets
	// (see nextSweepBlock), so this bounds the number of store round trips,
	// not how far the cursor can move: a long idle gap is crossed in a
	// handful of calls because block size grows by powers of ten.
	authFactorChallengeSweepLists = 8

	// authFactorChallengeSweepRecords bounds how many challenge records one
	// sweep inspects (Get) and reclaims (CompareAndDelete), whether or not
	// each turns out to be reclaimable.
	authFactorChallengeSweepRecords = 32
)

// sysChallengeSweepCursorKey persists the highest bucket number every record
// of which has been reclaimed. It is a sibling of sysChallengePrefix, not a
// child ("...challenge-sweep-cursor", not "...challenge/..."), so no
// challenge-namespace List ever returns it. Its value is a bucket number in
// the same fixed-width form as the challenge keys; an absent key means no
// bucket has been swept yet.
const sysChallengeSweepCursorKey = "__sys:authfactor:challenge-sweep-cursor"

// challengeSweepStore is the atomic capability set the sweep needs:
// CompareAndDelete to reclaim a record without racing a concurrent
// VerifyFactor, and PutIfAbsent/CompareAndSwap to advance the shared cursor
// without ever moving it backwards. KVServicer and kvstore.AtomicKVStore both
// declare all three with these exact signatures.
type challengeSweepStore interface {
	atomicChallengeStore
	PutIfAbsent(ctx context.Context, key string, value []byte) (stored bool, err error)
	CompareAndSwap(ctx context.Context, key string, expected, next []byte) (swapped bool, err error)
}

// challengeBucket returns the bucket number a challenge expiring at
// expiresAt is stored under when issued at now. The expiry is clamped to now
// first: a challenge issued already expired (a factor configured with a zero
// or negative TTL) is filed in the CURRENT bucket rather than a past one, so
// no record is ever written into a bucket the sweep may already have passed.
// VerifyFactor still enforces the record's own ExpiresAt; the clamp only
// decides where the record is filed.
func challengeBucket(expiresAt, now time.Time) int64 {
	if expiresAt.Before(now) {
		expiresAt = now
	}
	return expiresAt.Unix() / int64(authFactorChallengeBucketWidth/time.Second)
}

// formatChallengeBucket renders a bucket number in its fixed-width form.
func formatChallengeBucket(bucket int64) string {
	s := strconv.FormatInt(bucket, 10)
	if pad := authFactorChallengeBucketDigits - len(s); pad > 0 {
		s = strings.Repeat("0", pad) + s
	}
	return s
}

// parseChallengeBucket parses a fixed-width bucket number, reporting false
// for anything that is not exactly authFactorChallengeBucketDigits decimal
// digits.
func parseChallengeBucket(s string) (int64, bool) {
	if len(s) != authFactorChallengeBucketDigits {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// challengeKey is the storage key of a challenge record.
func challengeKey(bucket int64, factorChallengeID string) string {
	return sysChallengePrefix + formatChallengeBucket(bucket) + "/" + factorChallengeID
}

// challengeKeyBucket extracts the bucket number from a challenge record key,
// reporting false for a key that does not carry one.
func challengeKeyBucket(key string) (int64, bool) {
	rest, ok := strings.CutPrefix(key, sysChallengePrefix)
	if !ok || len(rest) <= authFactorChallengeBucketDigits || rest[authFactorChallengeBucketDigits] != '/' {
		return 0, false
	}
	return parseChallengeBucket(rest[:authFactorChallengeBucketDigits])
}

// challengePublicID is the identifier ChallengeFactor hands the caller.
func challengePublicID(bucket int64, factorChallengeID string) string {
	return formatChallengeBucket(bucket) + "." + factorChallengeID
}

// parseChallengeID derives a challenge's storage key from the identifier
// ChallengeFactor returned, reporting false when the identifier does not have
// the "<bucket>.<factor challenge id>" shape with a non-empty tail.
func parseChallengeID(challengeID string) (string, bool) {
	if len(challengeID) <= authFactorChallengeBucketDigits+1 || challengeID[authFactorChallengeBucketDigits] != '.' {
		return "", false
	}
	bucket, ok := parseChallengeBucket(challengeID[:authFactorChallengeBucketDigits])
	if !ok {
		return "", false
	}
	return challengeKey(bucket, challengeID[authFactorChallengeBucketDigits+1:]), true
}

// nextSweepBlock returns the largest aligned block of buckets that starts at
// next and ends no later than horizon, as the key prefix naming it and the
// last bucket it covers. A block of 10^k buckets starting at a multiple of
// 10^k is exactly the set of keys sharing the leading
// (authFactorChallengeBucketDigits - k) digits, so one List of that prefix
// returns every record in it and nothing outside it. The caller guarantees
// next <= horizon.
func nextSweepBlock(next, horizon int64) (prefix string, last int64) {
	size := int64(1)
	digits := authFactorChallengeBucketDigits
	for digits > 1 && next%(size*10) == 0 && next+size*10-1 <= horizon {
		size *= 10
		digits--
	}
	return sysChallengePrefix + formatChallengeBucket(next)[:digits], next + size - 1
}

// sweepExpiredChallenges reclaims challenge records nobody ever answered, so
// that a challenge issued and never verified does not occupy storage forever
// (go-qrdb#464 round 4).
//
// Its cost is independent of how many challenges are OUTSTANDING. It reads a
// persisted cursor (sysChallengeSweepCursorKey) naming the last bucket fully
// reclaimed, and walks forward from there only through buckets strictly older
// than bucket(now) - authFactorChallengeSweepGraceBuckets — every record in
// which has already expired. A bucket that can still hold a live challenge is
// never listed, so the number of live challenges has no effect on this
// call's List, Get or CompareAndDelete count. Per call it makes at most one
// cursor Get, authFactorChallengeSweepLists List calls,
// authFactorChallengeSweepRecords record Gets and CompareAndDeletes, and one
// cursor write.
//
// Each List covers the largest aligned block of expired buckets available
// (nextSweepBlock), so empty history — an idle tenant, or an absent cursor,
// which starts the walk at bucket zero — is crossed in a logarithmic number
// of Lists rather than one per bucket.
//
// Records are processed in key order, which is bucket order. When the record
// budget runs out, or a record cannot be reclaimed on this pass (a read
// fails, or by this node's clock it has not yet expired), the cursor stops
// just before that record's bucket so a later call resumes there; nothing is
// skipped. The cursor is advanced with PutIfAbsent (first sweep) or
// CompareAndSwap from the exact value read, so of two concurrent sweeps the
// loser's write is simply refused: the cursor never moves backwards, and the
// work both did is idempotent because every delete is a CompareAndDelete
// guarded on the bytes just read.
//
// It never fails the caller's ChallengeFactor request: every failure here
// concerns records unrelated to the challenge being issued. A store without
// the atomic capability set (challengeSweepStore) is not swept at all, rather
// than swept with unguarded deletes that could race a concurrent
// VerifyFactor.
func (s *authFactorService) sweepExpiredChallenges(ctx context.Context) {
	store, ok := s.kv.(challengeSweepStore)
	if !ok {
		return
	}
	cursorRaw, err := s.readRecord(ctx, sysChallengeSweepCursorKey)
	if err != nil {
		return
	}
	cursor := int64(-1)
	if len(cursorRaw) > 0 {
		if b, ok := parseChallengeBucket(string(cursorRaw)); ok {
			cursor = b
		}
		// An unparsable cursor restarts the walk from bucket zero; the
		// CompareAndSwap below replaces it from its exact current bytes.
	}

	now := s.now()
	horizon := challengeBucket(now, now) - authFactorChallengeSweepGraceBuckets - 1
	advanced := cursor
	budget := authFactorChallengeSweepRecords

walk:
	for lists := 0; lists < authFactorChallengeSweepLists && budget > 0 && advanced < horizon; lists++ {
		prefix, last := nextSweepBlock(advanced+1, horizon)
		keys, err := s.kv.List(ctx, prefix)
		if err != nil {
			break
		}
		slices.Sort(keys)
		for _, key := range keys {
			bucket, ok := challengeKeyBucket(key)
			if !ok {
				continue // not a challenge record key; nothing to reclaim
			}
			if budget == 0 || !s.reclaimChallenge(ctx, store, key, now) {
				advanced = max(advanced, bucket-1)
				break walk
			}
			budget--
		}
		advanced = last
	}

	if advanced <= cursor {
		return
	}
	next := []byte(formatChallengeBucket(advanced))
	if len(cursorRaw) == 0 {
		_, _ = store.PutIfAbsent(ctx, sysChallengeSweepCursorKey, next)
		return
	}
	_, _ = store.CompareAndSwap(ctx, sysChallengeSweepCursorKey, cursorRaw, next)
}

// reclaimChallenge deletes one challenge record found in an expired bucket
// and reports whether the sweep may move past it. A record already gone
// (consumed by VerifyFactor, or reclaimed by a concurrent sweep) counts as
// reclaimed; so does one whose bytes are not a challenge at all, since it
// can never be verified. A record that by this node's clock has not expired
// yet, or one whose read or delete fails, is left for a later pass.
func (s *authFactorService) reclaimChallenge(ctx context.Context, store challengeSweepStore, key string, now time.Time) bool {
	data, err := s.readRecord(ctx, key)
	if err != nil {
		return false
	}
	if len(data) == 0 {
		return true
	}
	var c authfactors.Challenge
	if err := json.Unmarshal(data, &c); err == nil && !c.ExpiresAt.Before(now) {
		return false
	}
	if _, err := store.CompareAndDelete(ctx, key, data); err != nil {
		return false
	}
	return true
}

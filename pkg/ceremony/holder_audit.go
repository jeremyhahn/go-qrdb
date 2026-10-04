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

package ceremony

import (
	"context"
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// The share-holder events every holder scope records on the compliance audit
// trail (HolderDeps.Audit). Each names the owning tenant (TenantID), the scope
// and the holder; none carries a share, an envelope, a PIN, a factor response
// or any key.
const (
	// HolderAuditResource is the Resource of every share-holder event.
	HolderAuditResource = "share-holder"

	// HolderAuditComponent labels a share-holder record that could not be
	// written, on qrdb_audit_write_failures_total{component=...}.
	HolderAuditComponent = "share-holder"

	// ActionHolderRegister records a holder enrolment.
	ActionHolderRegister = "holder-register"

	// ActionHolderClaim records a holder's share-envelope claim.
	ActionHolderClaim = "holder-claim"

	// ActionHolderQuorumSubmit records a holder's authentication for a
	// share submission to a quorum operation (attribute "op").
	ActionHolderQuorumSubmit = "holder-quorum-submit"

	// ActionHolderRekeyStart records the opening of a rekey round, a
	// revocation included (attribute "holder" names the holder removed).
	ActionHolderRekeyStart = "holder-rekey-start"

	// ActionHolderRevoked records a holder's removal, once the rekey that
	// revoked it is durable.
	ActionHolderRevoked = "holder-revoked"

	// ActionHolderUnenrol records a holder's removal before the barrier is
	// initialized, when it holds no share.
	ActionHolderUnenrol = "holder-unenrol"
)

// holderInvalidName stands in the trail for a holder name that is not a
// valid principal name: the name comes from the caller, unauthenticated on a
// refusal, and the trail records no caller-chosen text but a valid name.
const holderInvalidName = "(invalid-name)"

// Actors recorded when no authenticated identity performed the operation.
const (
	// holderActorAnonymous is a sealed-window caller that has not proved
	// itself to be any holder.
	holderActorAnonymous = "anonymous"

	// holderActorQuorum is the quorum of holders whose shares completed a
	// rekey: no single caller performed the revocation it commits.
	holderActorQuorum = "share-holder-quorum"
)

// holderEvent is one share-holder event, before the scope's identity is
// added to it.
type holderEvent struct {
	action string
	// holder is the holder the event is about.
	holder string
	// actor is who performed the operation when the request context carries
	// no authenticated identity: the enroller, a holder that proved itself,
	// or the quorum. Empty records holderActorAnonymous.
	actor string
	// attrs are event-specific attributes. They carry names and counts only.
	attrs map[string]any
	// err is the operation's failure; nil records success.
	err error
	// denied records a failure as a refused authentication rather than a
	// failed operation.
	denied bool
}

// event renders ev as the compliance record of the scope, keyed by the
// scope's owning tenant.
func (h *holderScope) event(ctx context.Context, ev holderEvent) audit.AuditEvent {
	attrs := make(map[string]any, len(ev.attrs)+3)
	for k, v := range ev.attrs {
		attrs[k] = v
	}
	attrs["scope"] = h.scope.String()
	attrs["domain"] = string(h.domain)
	if ev.holder != "" {
		attrs["holder"] = holderName(ev.holder)
	}
	return audit.AuditEvent{
		Actor:      holderActor(ctx, holderName(ev.actor)),
		TenantID:   tenantid.Normalize(h.tenantID),
		Resource:   HolderAuditResource,
		Action:     ev.action,
		Outcome:    holderOutcome(ev.err, ev.denied),
		Attributes: attrs,
	}
}

// record writes ev on the scope's compliance trail as a POST-HOC record: the
// operation has already happened -- or been refused -- so a sink that refuses
// the record is counted and logged (audit.EmitBestEffort), never returned as
// the operation's failure. Refusals, quorum submissions and revocations are
// recorded so. Without a sink it records nothing.
func (h *holderScope) record(ctx context.Context, d *HolderDeps, ev holderEvent) {
	if d == nil || d.Audit == nil {
		return
	}
	audit.EmitBestEffort(ctx, d.Audit, HolderAuditComponent, h.event(ctx, ev))
}

// gate writes ev on the scope's compliance trail as a GATING record, before
// the operation it describes takes effect: a registration before the
// certificate is issued, a claim before the envelope leaves the store, a rekey
// before its round opens. The record's outcome is audit.OutcomePermitted --
// the operation was admitted, not that it succeeded -- and the caller writes
// the operation's own outcome after the effect (settle), so a gated operation
// leaves "permitted" followed by "success" or "failure", and the
// hash-chained trail never carries a success that did not happen.
//
// It follows the node's one audit failure posture (HolderDeps.AuditPolicy,
// auth.audit_failure_policy): fail-closed -- the default -- returns
// *audit.GateError when the record cannot be written, and the caller refuses
// the operation with nothing changed; fail-open lets it proceed, counting the
// gap. Without a sink it records nothing.
func (h *holderScope) gate(ctx context.Context, d *HolderDeps, ev holderEvent) error {
	if d == nil || d.Audit == nil {
		return nil
	}
	rec := h.event(ctx, ev)
	rec.Outcome = audit.OutcomePermitted
	return audit.EmitGated(ctx, d.Audit, HolderAuditComponent, d.AuditPolicy, rec)
}

// settle records the outcome of a gated operation once it has run: its
// success or its failure, after the gate's "permitted", or a refusal before
// the gate. A gate refusal is not recorded again -- the sink that refused the
// gating record would refuse this one too, and the operation did not run.
func (h *holderScope) settle(ctx context.Context, d *HolderDeps, ev holderEvent, err error) {
	if isGateRefusal(err) {
		return
	}
	ev.err = err
	h.record(ctx, d, ev)
}

// isGateRefusal reports whether err is the trail's refusal of a gating
// record: the operation did not take effect and there is nothing more to
// record (the sink that refused would refuse again).
func isGateRefusal(err error) bool {
	var gate *audit.GateError
	return errors.As(err, &gate)
}

// holderName returns name when it is a valid principal name and
// holderInvalidName otherwise, so a caller-chosen string of any length or
// alphabet never reaches the trail.
func holderName(name string) string {
	if name == "" || validPrincipalName(name) {
		return name
	}
	return holderInvalidName
}

// holderActor names who performed a holder operation: the authenticated
// identity on ctx, else fallback, else holderActorAnonymous.
func holderActor(ctx context.Context, fallback string) string {
	if id := auth.IdentityFromContext(ctx); id != nil && id.Subject != "" {
		return id.Subject
	}
	if fallback != "" {
		return fallback
	}
	return holderActorAnonymous
}

// holderOutcome renders err as an audit outcome: success, or the denied or
// failure class with a FIXED reason token (holderReason). An error's own text
// never reaches the trail: a factor, HPKE or storage error is free text from
// code outside this package -- a go-xkms FIDO2, PIV, PKCS#11 or TPM factor
// among it -- and the trail is tenant-readable, hash-chained and cannot be
// redacted.
func holderOutcome(err error, denied bool) string {
	switch {
	case err == nil:
		return audit.OutcomeSuccess
	case denied:
		return audit.OutcomeDenied + ":" + holderReason(err)
	default:
		return audit.OutcomeFailure + ":" + holderReason(err)
	}
}

// Fixed reason tokens a refused holder operation is recorded with.
const (
	reasonNotFound           = "not-found"
	reasonChallengeMismatch  = "challenge-mismatch"
	reasonExpired            = "expired"
	reasonVerificationFailed = "verification-failed"
	reasonLimitReached       = "limit-reached"
	reasonDuplicate          = "duplicate"
	reasonSelfEnrolment      = "self-enrolment"
	reasonSealed             = "sealed"
	reasonInvalidRequest     = "invalid-request"
	reasonFactorNotAdmitted  = "factor-not-admitted"
	reasonCAUnavailable      = "ca-unavailable"
	reasonRoundInProgress    = "round-in-progress"
	reasonBreaksQuorum       = "breaks-quorum"
	reasonStaleRoster        = "stale-roster"
	reasonRosterMissing      = "roster-missing"
	reasonAuditUnavailable   = "audit-unavailable"
	reasonInitialized        = "initialized"
	reasonInitInProgress     = "initialization-in-progress"
	reasonUnknown            = "unknown"
)

// holderReasonSentinels maps each sentinel a holder operation refuses with to
// its token.
var holderReasonSentinels = map[error]string{
	ErrOfficerNotFound:             reasonNotFound,
	ErrNoPendingShare:              reasonNotFound,
	ErrChallengeNotFound:           reasonNotFound,
	ErrQuorumChallengeNotFound:     reasonNotFound,
	ErrQuorumChallengeMismatch:     reasonChallengeMismatch,
	ErrCertThumbprintMismatch:      reasonChallengeMismatch,
	ErrChallengeExpired:            reasonExpired,
	ErrShareExpired:                reasonExpired,
	ErrChallengeVerificationFailed: reasonVerificationFailed,
	ErrTooManyPendingChallenges:    reasonLimitReached,
	ErrDuplicateUsername:           reasonDuplicate,
	ErrDuplicateHolderKey:          reasonDuplicate,
	ErrSelfEnrolment:               reasonSelfEnrolment,
	ErrRegistrationWhileSealed:     reasonSealed,
	ErrMissingUsername:             reasonInvalidRequest,
	ErrMissingCSRPEM:               reasonInvalidRequest,
	ErrInvalidHolderRole:           reasonInvalidRequest,
	ErrCANotInitialized:            reasonCAUnavailable,
	seal.ErrQuorumRoundInProgress:  reasonRoundInProgress,
	seal.ErrHolderRosterMissing:    reasonRosterMissing,
	ErrUnenrolInitialized:          reasonInitialized,
	ErrInitializationInProgress:    reasonInitInProgress,
}

// holderReasonTypes maps each typed error a holder operation refuses with to
// its token, in the order they are tested. A factor's own refusal is
// factorRefusedError, which authenticate wraps around whatever the factor
// returned, so an injected factor's error is verification-failed however it
// reads.
var holderReasonTypes = []struct {
	is    func(error) bool
	token string
}{
	{isType[*audit.GateError], reasonAuditUnavailable},
	{isType[*factorRefusedError], reasonVerificationFailed},
	{isType[*authfactors.VerificationFailedError], reasonVerificationFailed},
	{isType[*QuorumRateLimitedError], reasonLimitReached},
	{isType[*HolderLimitReachedError], reasonLimitReached},
	{isType[*InvalidHolderNameError], reasonInvalidRequest},
	{isType[*CSRParseError], reasonInvalidRequest},
	{isType[*UnsupportedHolderKeyError], reasonInvalidRequest},
	{isType[*EncryptionKeyRequiredError], reasonInvalidRequest},
	{isType[*DuplicateFactorError], reasonInvalidRequest},
	{isType[*HolderFactorEnrollmentError], reasonInvalidRequest},
	{isType[*FactorNotEnrolledError], reasonFactorNotAdmitted},
	{isType[*seal.FactorNotAdmittedError], reasonFactorNotAdmitted},
	{isType[*authfactors.FactorNotFoundError], reasonFactorNotAdmitted},
	{isType[*seal.HolderNotInRosterError], reasonNotFound},
	{isType[*seal.RevocationBreaksQuorumError], reasonBreaksQuorum},
	{isType[*seal.StaleRosterError], reasonStaleRoster},
}

// isType reports whether err's chain holds a T.
func isType[T error](err error) bool {
	var target T
	return errors.As(err, &target)
}

// holderReason returns err's fixed reason token; reasonUnknown for an error
// no table names.
func holderReason(err error) string {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if token, ok := holderReasonSentinels[e]; ok {
			return token
		}
	}
	for _, t := range holderReasonTypes {
		if t.is(err) {
			return t.token
		}
	}
	return reasonUnknown
}

// factorRefusedError is a factor's refusal of a holder's response, carrying
// the factor's own error for errors.Is and errors.As. It marks the error as
// the factor's, so the trail records verification-failed and never the
// factor's text.
type factorRefusedError struct {
	err error
}

// Error returns the factor's own description.
func (e *factorRefusedError) Error() string { return e.err.Error() }

// Unwrap returns the factor's error.
func (e *factorRefusedError) Unwrap() error { return e.err }

// rekeyStartEvents returns the holder-rekey-start events of params: one per
// holder removed, or one for a rekey that removes nobody.
func rekeyStartEvents(params seal.RekeyParams) []holderEvent {
	attrs := map[string]any{"op": string(seal.OpRekey), "threshold": params.Threshold}
	if params.Policy != "" {
		attrs["policy"] = string(params.Policy)
	}
	if len(params.Remove) == 0 {
		return []holderEvent{{action: ActionHolderRekeyStart, attrs: attrs}}
	}
	evs := make([]holderEvent, len(params.Remove))
	for i, name := range params.Remove {
		evs[i] = holderEvent{action: ActionHolderRekeyStart, holder: name, attrs: attrs}
	}
	return evs
}

// startRekey opens the rekey round of params on barrier through open
// (seal.StorageBarrier.StartRekey) with the opening recorded on the trail as
// a GATING record: the barrier validates the rekey, then calls back
// (RekeyParams.Admit) and "permitted" is written before the round opens. A
// rekey the trail cannot record under fail-closed is refused with
// *audit.GateError and no round is opened. Once open returns, its outcome is
// recorded (settle): "success" for an opened round, or the failure -- of a
// rekey the barrier refused before admitting it, or one that failed after.
func (h *holderScope) startRekey(ctx context.Context, d *HolderDeps, params seal.RekeyParams, open func(seal.RekeyParams) (*seal.QuorumRoundInfo, error)) (*seal.QuorumRoundInfo, error) {
	params.Admit = func() error {
		for _, ev := range rekeyStartEvents(params) {
			if err := h.gate(ctx, d, ev); err != nil {
				return err
			}
		}
		return nil
	}
	info, err := open(params)
	for _, ev := range rekeyStartEvents(params) {
		h.settle(ctx, d, ev, err)
	}
	return info, err
}

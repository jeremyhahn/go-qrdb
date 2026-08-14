// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
//  1. GNU Affero General Public License v3.0 (AGPL-3.0)
//     See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
//  2. Commercial License
//     Contact licensing@automatethethings.com for commercial licensing options.

package ceremony

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
)

// This file decides which LOGIN credentials an administrator is enrolled with
// during the initialization ceremony.
//
// It is deliberately separate from the seal/unseal strategies in this package.
// A seal strategy answers "how is the barrier key protected at rest"; an admin
// factor answers "how does a human prove who they are". Conflating them
// produced an init flow that could unseal a database nobody could log into.
//
// This file owns only the ORDERING and the PREFLIGHT. It deliberately defines
// no enrollment interface and no registry: pkg/authfactors already has both
// (Factor.Enroll and FactorRegistry), and go-xkms already implements every
// factor against them. A second enrollment seam here would be the same
// contract under different names, kept in sync by hand.
//
// What is genuinely missing upstream, and therefore lives here: which factors
// to enroll for the administrator, in what order, and whether every one of
// them can succeed before the ceremony changes any state.

// AdminFactor identifies one way an administrator authenticates.
//
// It is an ALIAS of auth.AuthFactorType, not a parallel type. Enrollment and
// login must speak one vocabulary: an administrator who enrolls an "mtls"
// factor authenticates with an "mtls" factor, and the string an operator types
// at init is the string stored on the user record. A separate enrollment-only
// vocabulary would need a translation table that could silently drift.
type AdminFactor = auth.AuthFactorType

const (
	// FactorMTLS enrolls an X.509 client certificate for mTLS
	// authentication. This is the default and the baseline: it is the only
	// factor that works with no hardware attached to the machine running the
	// ceremony, and it is what the bootstrap admin bundle carries.
	FactorMTLS = auth.AuthFactorMTLS

	// FactorFIDO2 enrolls a WebAuthn/FIDO2 credential — a passkey — over
	// CTAP2, with no browser involved. The authenticator must be attached to
	// the machine running the ceremony.
	FactorFIDO2 = auth.AuthFactorFIDO2

	// FactorPIV enrolls a credential held in a PIV smartcard slot.
	FactorPIV = auth.AuthFactorPIV
)

// EnrollableAdminFactors is the set of factors an administrator may enroll
// during the initialization ceremony.
//
// It is deliberately NARROWER than auth.AuthFactorType's full set: only
// strong, hardware-backed, standards-based mechanisms an operator can present
// at first boot. AuthFactorPKCS11 is a valid login factor but is not
// bootstrappable this way — an HSM-held admin credential is enrolled after the
// system is running, against a configured backend.
//
// Nothing password-shaped appears here, or anywhere: this stack is passwordless
// by policy, not by default.
func EnrollableAdminFactors() []AdminFactor {
	return []AdminFactor{FactorMTLS, FactorFIDO2, FactorPIV}
}

// IsEnrollableAdminFactor reports whether f may be enrolled at init.
func IsEnrollableAdminFactor(f AdminFactor) bool {
	switch f {
	case FactorMTLS, FactorFIDO2, FactorPIV:
		return true
	default:
		return false
	}
}

// FactorAvailability is an OPTIONAL capability an authfactors.Factor may
// implement to report whether its enrollment can proceed right now.
//
// Optional, and checked by type assertion, so no existing factor has to
// change: a factor that cannot fail for environmental reasons (mTLS, which
// needs only a CA) simply does not implement it and is always considered
// available. A factor that depends on attached hardware (FIDO2, PIV) does,
// and that is what lets the ceremony refuse before changing any state.
type FactorAvailability interface {
	// Available reports whether enrollment can proceed, with operator-facing
	// text when it cannot ("no FIDO2 authenticator detected").
	Available(ctx context.Context) (ok bool, reason string)
}

// EnrollmentPlan is the ordered set of factors to enroll for the
// administrator.
//
// The zero value is not usable; build one with NewEnrollmentPlan so the mTLS
// baseline and the no-duplicates rule are enforced in one place.
type EnrollmentPlan struct {
	factors []AdminFactor
}

// NewEnrollmentPlan validates and normalises a requested factor list.
//
// An empty request yields the mTLS baseline, which is what makes "just
// initialize it" keep working. Duplicates collapse. The order is the caller's,
// except that FactorMTLS is always enrolled first when present: the client
// certificate is what the other factors' enrollment is authenticated and
// audited against, and it is the credential that still works if a hardware
// ceremony is interrupted.
func NewEnrollmentPlan(requested []AdminFactor) (*EnrollmentPlan, error) {
	if len(requested) == 0 {
		return &EnrollmentPlan{factors: []AdminFactor{FactorMTLS}}, nil
	}

	seen := make(map[AdminFactor]bool, len(requested))
	ordered := make([]AdminFactor, 0, len(requested))
	for _, f := range requested {
		if !IsEnrollableAdminFactor(f) {
			return nil, &UnsupportedFactorError{Factor: f}
		}
		if seen[f] {
			continue
		}
		seen[f] = true
		ordered = append(ordered, f)
	}

	if seen[FactorMTLS] && ordered[0] != FactorMTLS {
		rest := make([]AdminFactor, 0, len(ordered))
		for _, f := range ordered {
			if f != FactorMTLS {
				rest = append(rest, f)
			}
		}
		ordered = append([]AdminFactor{FactorMTLS}, rest...)
	}

	return &EnrollmentPlan{factors: ordered}, nil
}

// Factors returns the planned factors in enrollment order.
func (p *EnrollmentPlan) Factors() []AdminFactor {
	if p == nil {
		return []AdminFactor{FactorMTLS}
	}
	out := make([]AdminFactor, len(p.factors))
	copy(out, p.factors)
	return out
}

// Includes reports whether f is in the plan.
func (p *EnrollmentPlan) Includes(f AdminFactor) bool {
	if p == nil {
		return f == FactorMTLS
	}
	for _, planned := range p.factors {
		if planned == f {
			return true
		}
	}
	return false
}

// Preflight checks every planned factor against the canonical auth-factor
// registry BEFORE the ceremony changes any state.
//
// This ordering is the point of the method. Enrolling factor by factor and
// failing partway leaves an initialized barrier with a half-enrolled
// administrator — recoverable only by wiping the data directory. Refusing up
// front, with the specific reason, costs an operator one retry instead.
//
// Availability is consulted only when the registered factor implements
// FactorAvailability; anything else is taken to be available.
func (p *EnrollmentPlan) Preflight(ctx context.Context, reg *authfactors.FactorRegistry) error {
	if reg == nil {
		return ErrNilFactorRegistry
	}
	for _, f := range p.Factors() {
		factor, err := reg.Lookup(f.String())
		if err != nil {
			return &UnsupportedFactorError{Factor: f}
		}
		probe, ok := factor.(FactorAvailability)
		if !ok {
			continue
		}
		if available, reason := probe.Available(ctx); !available {
			return &FactorUnavailableError{Factor: f, Reason: reason}
		}
	}
	return nil
}

// AdminEnrollmentParams supplies the per-factor enrollment inputs the caller
// must produce, keyed by factor.
//
// The signature is what keeps hardware out of this package. Enrolling a FIDO2
// credential means driving a USB authenticator over CTAP2, and enrolling PIV
// means talking to a smartcard — neither belongs in a database. The consumer
// that owns those implementations (go-xkms) supplies the finished inputs; this
// package only decides which factors run and in what order.
//
// A provider may return nil params for a factor whose enrollment needs none.
type AdminEnrollmentParams func(ctx context.Context, factor AdminFactor) (map[string]any, error)

// AdminEnrollmentResult pairs a factor with the enrollment it produced.
type AdminEnrollmentResult struct {
	// Factor is the factor that was enrolled.
	Factor AdminFactor

	// Enrollment is the credential record the factor produced, ready to
	// persist against the administrator.
	Enrollment *authfactors.Enrollment
}

// EnrollAdminFactors runs the plan's factors in order against the canonical
// registry and returns what was enrolled.
//
// Callers MUST have run Preflight first, before any state-changing ceremony
// work: this function enrolls factor by factor, so a hardware failure halfway
// through leaves the earlier factors enrolled. Preflight is what turns that
// from a likely outcome into an unlikely one.
//
// Enrollment stops at the first error and returns what succeeded alongside it,
// so a caller can report precisely which factors are live rather than implying
// the administrator has none.
//
// persister gives each enrollment a home. Factor.Enroll writes nothing, so
// without one the credential ends up on the operator's authenticator and
// nowhere on the server -- which is how a passkey could enroll "successfully"
// and then be unable to authenticate. Nil is permitted and preserves that
// older, probe-only behaviour.
func EnrollAdminFactors(
	ctx context.Context,
	plan *EnrollmentPlan,
	reg *authfactors.FactorRegistry,
	userID, tenantID string,
	params AdminEnrollmentParams,
	persister AdminFactorPersister,
) ([]AdminEnrollmentResult, error) {
	if reg == nil {
		return nil, ErrNilFactorRegistry
	}

	results := make([]AdminEnrollmentResult, 0, len(plan.Factors()))
	for _, f := range plan.Factors() {
		factor, err := reg.Lookup(f.String())
		if err != nil {
			return results, &UnsupportedFactorError{Factor: f}
		}

		var methodParams map[string]any
		if params != nil {
			methodParams, err = params(ctx, f)
			if err != nil {
				return results, &FactorEnrollmentError{Factor: f, Err: err}
			}
		}

		enrollment, err := factor.Enroll(ctx, &authfactors.EnrollParams{
			UserID:       userID,
			TenantID:     tenantID,
			MethodParams: methodParams,
		})
		if err != nil {
			return results, &FactorEnrollmentError{Factor: f, Err: err}
		}
		if persister != nil {
			if perr := persister.PersistAdminFactor(ctx, f, enrollment); perr != nil {
				return results, &FactorEnrollmentError{Factor: f, Err: perr}
			}
		}

		results = append(results, AdminEnrollmentResult{Factor: f, Enrollment: enrollment})
	}
	return results, nil
}

// AdminFactorPersister writes an enrollment into the caller's factor store.
//
// Factor.Enroll is pure: it drives the authenticator and returns public
// credential material, writing nothing. Something has to give that result a
// home, or the credential exists on the operator's token and nowhere on the
// server -- enrolled in appearance only, and unverifiable at login. That was
// the state of admin enrollment before this seam existed.
//
// It is an interface rather than a concrete store because the record belongs to
// the consumer's identity stack: qrdb's own service layer implements it, and so
// does any deployment that keeps users elsewhere. Passing nil is allowed and
// means "return the enrollments, persist nothing" -- the previous behaviour,
// kept for callers that genuinely only want to probe hardware.
type AdminFactorPersister interface {
	// PersistAdminFactor stores one enrolled factor for the administrator.
	//
	// It is called once per factor, immediately after that factor enrolls and
	// before the next one starts, so a failure halfway through leaves the
	// earlier factors both enrolled and persisted rather than enrolled and
	// lost.
	PersistAdminFactor(ctx context.Context, factor AdminFactor, enrollment *authfactors.Enrollment) error
}

// AdminFactorPersisterFunc adapts a plain function into an
// AdminFactorPersister.
type AdminFactorPersisterFunc func(ctx context.Context, factor AdminFactor, enrollment *authfactors.Enrollment) error

// PersistAdminFactor implements AdminFactorPersister.
func (f AdminFactorPersisterFunc) PersistAdminFactor(ctx context.Context, factor AdminFactor, enrollment *authfactors.Enrollment) error {
	return f(ctx, factor, enrollment)
}

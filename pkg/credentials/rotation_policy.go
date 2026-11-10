// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package credentials

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// rotationMetaRetries bounds the compare-and-swap retries of a policy
// update. Each retry follows a write that changed the record, so the bound
// is only reached under sustained concurrent writes to one credential.
const rotationMetaRetries = 8

// rotationClaimLease is how long a scheduled rotation's claim excludes
// every other caller. The holder runs the value source and the rotation
// under a deadline of the same length, so it has stopped writing before
// another node may take an expired claim, and a claimant that crashed
// holds the credential for at most one lease.
const rotationClaimLease = 5 * time.Minute

// rotationSettleTimeout bounds the one metadata update that releases the
// claim of a failed rotation, which runs past the scheduler's own context. A
// rotation that lands ends its claim in its own version write, under the
// lease. It is a single
// compare-and-swap of a small record; one that cannot land in this time
// leaves the claim to lapse.
const rotationSettleTimeout = 30 * time.Second

// rotationClaim is the lease a scheduled rotation takes on a credential's
// metadata record before it invokes the rotation value source, so one
// caller at a time runs a source that may have side effects outside the
// store (a changed database password, a replacement API key).
type rotationClaim struct {
	// ID identifies the holder; only a write naming it ends the claim.
	ID string `json:"id"`

	// Due is the NextRotation the claim was taken for. The completed
	// rotation advances the schedule only while it still reads Due, so
	// a policy change made meanwhile keeps its own schedule.
	Due time.Time `json:"due"`

	// Until is when the claim lapses and another caller may take it.
	Until time.Time `json:"until"`
}

// RotationPolicy is a named credential's scheduled-rotation policy. It is
// stored in the credential's metadata record beside its version history.
type RotationPolicy struct {
	// Enabled turns scheduled rotation on. A disabled policy never
	// rotates and needs no valid Interval or RetainVersions.
	Enabled bool `json:"enabled"`

	// Interval is the time between rotations. Must be positive when
	// Enabled.
	Interval time.Duration `json:"interval"`

	// RetainVersions is how many versions, the current one included,
	// survive a scheduled rotation. Must be at least 1 when Enabled.
	RetainVersions int `json:"retain_versions"`
}

// validate refuses an enabled policy the scheduler cannot carry out.
func (p RotationPolicy) validate() error {
	if !p.Enabled {
		return nil
	}
	if p.Interval <= 0 {
		return &InvalidRotationPolicyError{Reason: "interval must be positive"}
	}
	if p.RetainVersions < 1 {
		return &InvalidRotationPolicyError{Reason: "retain versions must be at least 1"}
	}
	return nil
}

// RotationValueSource produces the new value of a scheduled rotation of
// the named credential. go-qrdb generates no credential values: the
// composer that owns value generation (go-quicbaas) supplies the source.
// The service destroys the returned Buffer once the rotation is written.
type RotationValueSource func(ctx context.Context, name string) (*secure.Buffer, error)

// WithRotationValueSource sets the source RotateDue draws new values from.
// Without it RotateDue rotates nothing and reports ErrNoRotationValueSource
// for each due credential.
func WithRotationValueSource(src RotationValueSource) Option {
	return func(s *CredentialService) {
		s.rotationSource = src
	}
}

// SetRotationPolicy stores the named credential's rotation policy. Setting
// an enabled policy schedules the next rotation one Interval from now;
// setting a disabled one clears the schedule.
//
// It refuses an invalid policy, and any policy on a manual service, with
// *InvalidRotationPolicyError, and an unknown credential with
// ErrCredentialNotFound.
func (s *CredentialService) SetRotationPolicy(ctx context.Context, name string, policy RotationPolicy) error {
	if err := validateName(name); err != nil {
		return err
	}
	if s.strategy != StrategyBarrier {
		return &InvalidRotationPolicyError{Reason: "the " + s.strategy + " strategy holds non-resident values and cannot rotate on a schedule"}
	}
	if err := policy.validate(); err != nil {
		return err
	}
	if _, exists, err := s.loadBarrierCurrent(ctx, name); err != nil {
		return err
	} else if !exists {
		return ErrCredentialNotFound
	}

	var next time.Time
	if policy.Enabled {
		next = s.now().UTC().Add(policy.Interval)
	}
	if err := s.updateBarrierMeta(ctx, name, "write meta", func(meta *credentialMeta) bool {
		p := policy
		meta.Policy = &p
		meta.NextRotation = next
		return true
	}); err != nil {
		return err
	}

	s.emitAudit(ctx, "rotation_policy", name, map[string]any{
		"enabled":         policy.Enabled,
		"interval":        policy.Interval.String(),
		"retain_versions": policy.RetainVersions,
	})
	return nil
}

// RotationPolicy returns the named credential's rotation policy, the zero
// policy when none is set. An unknown credential is ErrCredentialNotFound.
func (s *CredentialService) RotationPolicy(ctx context.Context, name string) (RotationPolicy, error) {
	meta, err := s.rotationMeta(ctx, name)
	if err != nil || meta.Policy == nil {
		return RotationPolicy{}, err
	}
	return *meta.Policy, nil
}

// NextRotation returns when the named credential's enabled policy next
// falls due, the zero time when no enabled policy is set. An unknown
// credential is ErrCredentialNotFound.
func (s *CredentialService) NextRotation(ctx context.Context, name string) (time.Time, error) {
	meta, err := s.rotationMeta(ctx, name)
	if err != nil {
		return time.Time{}, err
	}
	return meta.NextRotation, nil
}

// rotationMeta loads the metadata record of an existing credential. A
// manual service keeps no policies, so an existing manual credential reads
// as an empty record.
func (s *CredentialService) rotationMeta(ctx context.Context, name string) (*credentialMeta, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	if s.strategy != StrategyBarrier {
		s.mu.RLock()
		_, exists := s.manualCreds[name]
		s.mu.RUnlock()
		if !exists {
			return nil, ErrCredentialNotFound
		}
		return &credentialMeta{}, nil
	}
	if _, exists, err := s.loadBarrierCurrent(ctx, name); err != nil {
		return nil, err
	} else if !exists {
		return nil, ErrCredentialNotFound
	}
	return s.loadBarrierMeta(ctx, name)
}

// RotateDue rotates every credential whose enabled policy is due at now:
// one RotateCredential with a value from the rotation source, a trim of
// the version history down to RetainVersions, and the next rotation
// scheduled one Interval after now. It returns how many credentials it
// rotated, and joins one *RotationError per due credential it could not
// rotate.
//
// Each rotation is claimed first, before the rotation source is invoked,
// by a compare-and-swap of the credential's metadata record that records
// an expiring claim, so when several nodes call RotateDue over the same
// barrier storage, exactly one of them invokes the source for a given due
// credential. The schedule advances only when the claimed rotation lands;
// a rotation that fails releases its claim and leaves the credential due,
// so the next call retries it, and a claim whose holder crashed lapses
// after rotationClaimLease and the credential is due again. A manual service has no policies and rotates
// nothing.
func (s *CredentialService) RotateDue(ctx context.Context, now time.Time) (int, error) {
	if s.strategy != StrategyBarrier {
		return 0, nil
	}
	names, err := s.listBarrier(ctx)
	if err != nil {
		return 0, err
	}
	now = now.UTC()
	rotated := 0
	var errs []error
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		done, err := s.rotateIfDue(ctx, name, now)
		if err != nil {
			errs = append(errs, &RotationError{Name: name, Cause: err})
			continue
		}
		if done {
			rotated++
		}
	}
	return rotated, errors.Join(errs...)
}

// rotateIfDue carries out one credential's policy if it is due at now. It
// reports whether this call rotated the credential.
func (s *CredentialService) rotateIfDue(ctx context.Context, name string, now time.Time) (bool, error) {
	claim, cleanup, err := s.claimRotation(ctx, name, now)
	if claim == nil {
		if cleanup != "" {
			err = errors.Join(err, s.cleanupRotation(ctx, name, cleanup))
		}
		return false, err
	}

	// The holder stops when its lease lapses, so no other caller takes
	// the claim while this one may still write. The remaining lease is
	// read on the clock the claim was taken on.
	rctx, cancel := context.WithTimeout(ctx, claim.Until.Sub(s.now()))
	next, err := s.rotateClaimed(rctx, name, claim, now)
	cancel()
	if err != nil {
		// The claim is released even when the scheduler's context ended,
		// the usual cause of a failed rotation; a release that fails
		// anyway is recovered when the claim lapses. Its timeout starts
		// once the rotation has returned, so a rotation that used its
		// whole lease leaves the release its own full bound.
		sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), rotationSettleTimeout)
		defer scancel()
		if rerr := s.releaseRotation(sctx, name, claim); rerr != nil {
			s.logger.Warn("rotation claim left to lapse",
				slog.String("name", name),
				slog.Time("until", claim.Until),
				slog.String("error", rerr.Error()))
		}
		return false, err
	}
	if err := s.cleanupRotation(ctx, name, claim.ID); err != nil {
		return true, err
	}
	s.logger.Info("credential rotated on schedule",
		slog.String("name", name),
		slog.Time("next_rotation", next))
	return true, nil
}

// cleanupRotation carries out the retention of the completed scheduled
// rotation id: it deletes the spent staged value, reaps expired versions,
// trims the history down to the RetainVersions of the policy as it stands at
// the trim (pruneBarrierHistory), and then clears the record's pending
// cleanup marker if it still names id. A failure leaves the marker, so the
// next check retries the cleanup without invoking the value source.
func (s *CredentialService) cleanupRotation(ctx context.Context, name, id string) error {
	meta, err := s.loadBarrierMeta(ctx, name)
	if err != nil {
		return err
	}
	// A value staged for the current version is spent. It lives under its
	// own key, so a rotation that retained a version since the read above
	// keeps it.
	if meta.CurrentVersion > 0 {
		if err := s.store.Delete(ctx, stagedKey(name, meta.CurrentVersion)); err != nil {
			return &OperationError{Op: "delete staged rotation", Cause: err}
		}
	}
	if _, err := s.GCExpiredVersions(ctx, name); err != nil {
		return err
	}
	if err := s.pruneBarrierHistory(ctx, name); err != nil {
		return err
	}
	return s.updateBarrierMeta(ctx, name, "finish cleanup", func(meta *credentialMeta) bool {
		if meta.PendingCleanup != id {
			return false
		}
		meta.PendingCleanup = ""
		return true
	})
}

// claimRotation takes the rotation claim on name when its enabled policy is
// due at now and no other caller holds an unexpired claim. It returns the
// claim, or a nil claim when this call must not rotate. With a nil claim it
// also returns the record's pending cleanup, the claim ID of a completed
// rotation whose retention still has to run; it returns none while another
// caller holds an unexpired claim, whose rotation cleans up after itself.
// An unexpired claim is judged, and a new claim's lease timed, at its
// acquisition on the service clock, not at now.
func (s *CredentialService) claimRotation(ctx context.Context, name string, now time.Time) (*rotationClaim, string, error) {
	var (
		claim    *rotationClaim
		cleanup  string
		noSource bool
	)
	err := s.updateBarrierMeta(ctx, name, "claim rotation", func(meta *credentialMeta) bool {
		claim, cleanup, noSource = nil, meta.PendingCleanup, false
		// The lease is timed from its acquisition, not from the pass's
		// now: one pass may have spent leases on earlier credentials.
		acquired := s.now().UTC()
		p := meta.Policy
		if p == nil || !p.Enabled || meta.NextRotation.After(now) {
			return false
		}
		if meta.Claim != nil && meta.Claim.Until.After(acquired) {
			// Another caller holds this rotation, and its cleanup.
			cleanup = ""
			return false
		}
		if s.rotationSource == nil {
			noSource = true
			return false
		}
		claim = &rotationClaim{ID: rand.Text(), Due: meta.NextRotation, Until: acquired.Add(rotationClaimLease)}
		cleanup = ""
		meta.Claim = claim
		return true
	})
	if err != nil {
		return nil, "", err
	}
	if noSource {
		return nil, cleanup, ErrNoRotationValueSource
	}
	return claim, cleanup, nil
}

// rotateClaimed draws a value from the rotation source and makes it the
// current version, keeping the previous one for the history trim, and
// returns the NextRotation it leaves. The value is staged under the next
// version before it becomes current, and the write that advances the version
// also ends claim and the interval it was taken for. An attempt that failed
// between the two leaves the staged value, which the next claimant lands
// instead of invoking the source again, so the source is called once per
// interval.
func (s *CredentialService) rotateClaimed(ctx context.Context, name string, claim *rotationClaim, now time.Time) (time.Time, error) {
	sched := &scheduledRotation{claim: claim, now: now}
	staged, err := s.stagedRotation(ctx, name)
	if err != nil {
		return time.Time{}, err
	}
	if staged != nil {
		defer secure.Wipe(staged)
		sched.resumed = true
		_, err := s.RotateCredential(ctx, name, RotateOptions{NewValue: staged, retainPrevious: true, schedule: sched})
		return sched.next, err
	}
	value, err := s.rotationSource(ctx, name)
	if err != nil {
		return time.Time{}, err
	}
	if value == nil {
		return time.Time{}, ErrEmptyCredentialValue
	}
	defer value.Destroy()
	err = value.WithBytes(func(b []byte) error {
		_, err := s.RotateCredential(ctx, name, RotateOptions{NewValue: b, retainPrevious: true, schedule: sched})
		return err
	})
	return sched.next, err
}

// stagedRotation returns the value an earlier scheduled rotation of name
// staged under the next version and did not land, or nil when there is none.
// Like the current record, a staged one the store cannot read is treated as
// absent.
func (s *CredentialService) stagedRotation(ctx context.Context, name string) ([]byte, error) {
	meta, err := s.loadBarrierMeta(ctx, name)
	if err != nil {
		return nil, err
	}
	current, exists, err := s.loadBarrierCurrent(ctx, name)
	if err != nil {
		return nil, err
	}
	secure.Wipe(current)
	_, next := rotationVersions(meta.CurrentVersion, exists)
	staged, err := s.store.Get(ctx, stagedKey(name, next))
	if errors.Is(err, seal.ErrSecretNotFound) {
		return nil, nil
	}
	if err != nil {
		// Any other failure is not absence: reading it as absence would
		// invoke the rotation source again for a value already staged.
		return nil, &OperationError{Op: "read staged rotation", Cause: err}
	}
	return staged, nil
}

// settle ends the claim in the metadata write that lands its rotation and
// schedules the next one an Interval after now, unless the policy or its
// schedule changed meanwhile. It records the NextRotation it leaves. A claim
// that lapsed and was taken by another caller is that caller's to end.
func (r *scheduledRotation) settle(meta *credentialMeta) {
	if meta.Claim != nil && meta.Claim.ID == r.claim.ID {
		meta.Claim = nil
		meta.PendingCleanup = r.claim.ID
		if p := meta.Policy; p != nil && p.Enabled && meta.NextRotation.Equal(r.claim.Due) {
			meta.NextRotation = r.now.Add(p.Interval)
		}
	}
	r.next = meta.NextRotation
}

// releaseRotation ends claim after its rotation failed, leaving NextRotation
// where it was, so the credential is due again at the next check rather
// than an Interval later.
func (s *CredentialService) releaseRotation(ctx context.Context, name string, claim *rotationClaim) error {
	return s.updateBarrierMeta(ctx, name, "release rotation", func(meta *credentialMeta) bool {
		if meta.Claim == nil || meta.Claim.ID != claim.ID {
			return false
		}
		meta.Claim = nil
		return true
	})
}

// pruneBarrierHistory deletes the non-current versions the policy no longer
// retains: all but the newest RetainVersions-1. History is appended in
// rotation order, so the newest are at its end. The policy is the one in the
// record read here, with the history it trims, and not the one the rotation
// was claimed under: an operator may have changed it while the value source
// ran, and a policy disabled or removed since trims nothing.
func (s *CredentialService) pruneBarrierHistory(ctx context.Context, name string) error {
	meta, err := s.loadBarrierMeta(ctx, name)
	if err != nil {
		return err
	}
	p := meta.Policy
	if p == nil || !p.Enabled {
		return nil
	}
	excess := len(meta.History) - (p.RetainVersions - 1)
	if excess <= 0 {
		return nil
	}
	reaped, err := s.reapBarrierVersions(ctx, name, meta.History[:excess])
	if err != nil {
		return err
	}
	s.emitAudit(ctx, "gc", name, map[string]any{"reaped": reaped})
	return nil
}

// updateBarrierMeta applies fn to the credential's metadata record with a
// compare-and-swap, so a concurrent claim, rotation, policy change or
// history trim is never overwritten. It is the one writer of the record:
// on a conflict it re-reads the record and applies fn again, so fn must
// derive its change from the record it is given. fn reports whether the
// record needs writing; false leaves it untouched. An absent record is
// created with a put-if-absent. A failed or contended write is reported as
// an *OperationError for op.
func (s *CredentialService) updateBarrierMeta(ctx context.Context, name, op string, fn func(*credentialMeta) bool) error {
	key := metaKey(name)
	for range rotationMetaRetries {
		var meta credentialMeta
		raw, getErr := s.store.Get(ctx, key)
		switch {
		case getErr == nil:
			if err := json.Unmarshal(raw, &meta); err != nil {
				return &OperationError{Op: "decode meta", Cause: err}
			}
		case !errors.Is(getErr, seal.ErrSecretNotFound):
			// Only an absent record starts from the zero value: fn must
			// not decide on metadata that could not be read.
			return &OperationError{Op: "read meta", Cause: getErr}
		}
		if !fn(&meta) {
			return nil
		}
		next, err := json.Marshal(&meta)
		if err != nil {
			return &OperationError{Op: "encode meta", Cause: err}
		}
		var written bool
		if getErr != nil {
			written, err = s.store.PutIfAbsent(ctx, key, next)
		} else {
			written, err = s.store.CompareAndSwap(ctx, key, raw, next)
		}
		if err != nil {
			return &OperationError{Op: op, Cause: err}
		}
		if written {
			return nil
		}
	}
	return &OperationError{Op: op, Cause: ErrRotationMetaContended}
}

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
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// =============================================================================
// Storage layout
// =============================================================================
//
// A versioned credential at logical name "<name>" is laid out as:
//
//	credentials/<name>              — the current value (always the latest)
//	credential-meta/<name>          — JSON metadata (current version + grace history)
//	credential-versions/<e>/<N>     — historical value for version N (kept while
//	                                   in grace period; deleted by GC after expiry)
//	credential-staged/<e>/<N>       — a scheduled rotation's staged value for
//	                                   version N, apart from the version records
//
// <e> is the name in unpadded URL-safe base64 and <N> the decimal version.
// A name may be any non-empty string, so no record is told apart by a
// character in its key: each kind has its own prefix, the value and metadata
// keys end in the whole name, and the encoded name holds no '/', so the one
// '/' after it separates it from the version. A key therefore names exactly
// one (kind, name, version), and no name can produce another record's key.
//
// The plain "credentials/<name>" entry is the existing key the
// non-versioned API has always used. Credentials originally written
// before rotation existed look like "<name> at v1 with no recorded
// metadata"; the first Rotate call promotes them by writing the metadata.

// metaSuffix and versionPrefix are the suffixes the node-local legacy store
// kept the metadata and version records under, after the name. Only the
// migration reads that layout; it also names a migrated record's parts by
// them.
const (
	metaSuffix    = "#meta"
	versionPrefix = "#v"
)

// =============================================================================
// Types
// =============================================================================

// CredentialVersion describes one version of a credential. The Value
// field is populated only when the caller explicitly retrieves the
// versioned data; ListCredentialVersions returns metadata-only entries
// with Value == nil.
type CredentialVersion struct {
	// Name is the credential's logical name.
	Name string `json:"name"`

	// Version is the monotonic version number, starting at 1.
	Version uint64 `json:"version"`

	// Value carries the credential bytes. Nil for metadata-only
	// responses (List + ListVersions).
	Value []byte `json:"-"`

	// CreatedAt records when the version was first written.
	CreatedAt time.Time `json:"created_at"`

	// IsCurrent reports whether this is the active/current version.
	// Exactly one entry per credential name is current at any time.
	IsCurrent bool `json:"is_current"`

	// GraceUntil is the wall-clock time at which a historical
	// version becomes unreachable. Zero when IsCurrent==true or
	// when the version was written without a grace period.
	GraceUntil time.Time `json:"grace_until,omitempty"`
}

// RotateOptions controls a single RotateCredential call.
type RotateOptions struct {
	// NewValue is the new credential value to store as the next
	// version. Must be non-empty.
	NewValue []byte

	// GracePeriod is how long the previously-current version
	// remains retrievable by version number after the rotation.
	// Zero means "no grace": the old version is wiped immediately
	// and only the new value is accessible.
	GracePeriod time.Duration

	// retainPrevious keeps the previously-current version with no grace
	// deadline, for a scheduled rotation whose policy retains versions by
	// count rather than by time.
	retainPrevious bool

	// importDigest, set by a migration import, is the digest of the legacy
	// copy the new value comes from. It is recorded in the same metadata
	// write that advances the version.
	importDigest string

	// schedule, set by a scheduled rotation, stages the new value before it
	// becomes current and settles the claim in the same metadata write that
	// advances the version.
	schedule *scheduledRotation
}

// scheduledRotation carries a scheduled rotation's claim into the write that
// completes it, and that write's result back out.
type scheduledRotation struct {
	claim *rotationClaim
	now   time.Time
	// resumed reports that the new value was read back from the staged
	// version an earlier attempt left, which is therefore not staged again.
	resumed bool
	// staged is the version the value is staged for, as the metadata
	// record names it. The rotation commits only while the record still
	// does.
	staged uint64
	// next is the schedule's next due time once the write settles it.
	next time.Time
}

// holds reports that meta still records this rotation: its claim, and
// the operation the claim ran the value source for. Every landing write
// clears RotationOp, so a replacement that lands while the claim is held
// fences the claim's value out; a retried claim of the same rotation
// keeps the operation and passes.
func (r *scheduledRotation) holds(meta *credentialMeta) bool {
	return meta.Claim != nil && meta.Claim.ID == r.claim.ID &&
		meta.RotationOp != "" && meta.RotationOp == r.claim.Op
}

// RotateResult describes the rotation outcome.
type RotateResult struct {
	// Name is the credential rotated.
	Name string

	// PreviousVersion is the version that was current before this
	// rotation. Zero if the credential did not exist before.
	PreviousVersion uint64

	// NewVersion is the version that is current after this
	// rotation. Always PreviousVersion + 1 (or 1 if there was no
	// previous version).
	NewVersion uint64

	// GraceUntil is when the previous version stops being
	// retrievable. Zero when there is no previous version or when
	// the caller specified GracePeriod == 0.
	GraceUntil time.Time
}

// credentialMeta is the on-disk JSON layout for the metadata record.
type credentialMeta struct {
	// CurrentVersion is the monotonic version of the value at
	// credentials/<name>.
	CurrentVersion uint64 `json:"current_version"`

	// History records non-current versions that have been written.
	// Entries are pruned when their grace period elapses.
	History []credentialMetaEntry `json:"history,omitempty"`

	// Policy is the credential's rotation policy, nil when none was set.
	Policy *RotationPolicy `json:"rotation_policy,omitempty"`

	// NextRotation is when an enabled Policy next falls due. Zero when
	// no enabled policy is set.
	NextRotation time.Time `json:"next_rotation,omitzero"`

	// Claim is the lease of the scheduled rotation in progress, nil when
	// none is. Only its holder invokes the rotation value source.
	Claim *rotationClaim `json:"rotation_claim,omitempty"`

	// RotationOp identifies the pending scheduled rotation's value-source
	// operation, empty when none is pending. The first claim of the
	// rotation writes it before the source runs, every later claim of the
	// same rotation keeps it, and any version's landing write clears it,
	// so a retry passes the source the operation it already performed.
	RotationOp string `json:"rotation_op,omitempty"`

	// PendingCleanup is the claim ID of a completed scheduled rotation
	// whose retention cleanup (expired-version GC and the trim to
	// RetainVersions) has not yet succeeded, empty when none is pending.
	// Every later check retries the cleanup, without invoking the value
	// source, until it succeeds.
	PendingCleanup string `json:"pending_cleanup,omitempty"`

	// Staged is the version a scheduled rotation's staged value is
	// pending for, 0 when none is. Only the staged value the record names
	// is resumed; every write of the current value clears it, so a value
	// staged before a replacement is never replayed over it.
	Staged uint64 `json:"staged,omitempty"`
}

type credentialMetaEntry struct {
	Version    uint64    `json:"version"`
	CreatedAt  time.Time `json:"created_at"`
	GraceUntil time.Time `json:"grace_until,omitempty"`
}

// =============================================================================
// Public API additions
// =============================================================================

// ListCredentials returns the names of every stored credential.
// Names only — values are never returned by enumeration.
//
// Returns ErrEnumerationNotSupported when the active strategy cannot
// enumerate (current strategies all can; reserved for future HSM-only
// strategies).
func (s *CredentialService) ListCredentials(ctx context.Context) ([]string, error) {
	if err := s.ensureMigrated(ctx); err != nil {
		return nil, err
	}
	switch s.strategy {
	case StrategyManual:
		s.mu.RLock()
		out := make([]string, 0, len(s.manualCreds))
		for name := range s.manualCreds {
			out = append(out, name)
		}
		s.mu.RUnlock()
		sortStrings(out)
		return out, nil
	case StrategyBarrier:
		return s.listBarrier(ctx)
	default:
		return nil, ErrInvalidStrategy
	}
}

// RotateCredential atomically replaces the current value of name with
// opts.NewValue. The previous value remains retrievable by version
// number via RetrieveCredentialVersion until opts.GracePeriod elapses;
// passing GracePeriod == 0 wipes the previous value immediately.
//
// If the credential does not exist yet RotateCredential behaves
// identically to StoreCredential and returns NewVersion == 1.
func (s *CredentialService) RotateCredential(ctx context.Context, name string, opts RotateOptions) (*RotateResult, error) {
	if err := s.ensureMigrated(ctx); err != nil {
		return nil, err
	}
	if err := validateName(name); err != nil {
		return nil, err
	}
	if err := validateValue(opts.NewValue); err != nil {
		return nil, err
	}
	if opts.GracePeriod < 0 {
		return nil, ErrInvalidGracePeriod
	}

	var result *RotateResult
	var err error
	switch s.strategy {
	case StrategyManual:
		result, err = s.rotateManual(ctx, name, opts)
	case StrategyBarrier:
		result, err = s.rotateBarrier(ctx, name, opts)
	default:
		return nil, ErrInvalidStrategy
	}
	if err != nil {
		return nil, err
	}

	s.emitAudit(ctx, "rotate", name, map[string]any{
		"previous_version": result.PreviousVersion,
		"new_version":      result.NewVersion,
		"grace_period":     opts.GracePeriod.String(),
	})
	return result, nil
}

// RetrieveCredentialVersionBuffer returns the stored credential value for a
// specific version in protected memory. Use version == 0 to mean "current
// version" (same as RetrieveCredentialBuffer). The caller owns the returned
// buffer and MUST Destroy it, on every path.
//
// Returns ErrVersionNotFound when the version was never written and
// ErrVersionExpired when the version was once present but its grace
// period has elapsed (GC may or may not have reaped the storage yet —
// either way the caller is denied).
func (s *CredentialService) RetrieveCredentialVersionBuffer(ctx context.Context, name string, version uint64) (*secure.Buffer, error) {
	if err := s.ensureMigrated(ctx); err != nil {
		return nil, err
	}
	if err := validateName(name); err != nil {
		return nil, err
	}

	if version == 0 {
		return s.RetrieveCredentialBuffer(ctx, name)
	}

	switch s.strategy {
	case StrategyManual:
		return s.retrieveManualVersion(ctx, name, version)
	case StrategyBarrier:
		return s.retrieveBarrierVersion(ctx, name, version)
	default:
		return nil, ErrInvalidStrategy
	}
}

// ListCredentialVersions returns the version history (current + all
// non-expired grace versions) for name. Each entry's Value field is
// nil; use RetrieveCredentialVersionBuffer to read a specific value.
//
// Returns ErrCredentialNotFound when name does not exist.
func (s *CredentialService) ListCredentialVersions(ctx context.Context, name string) ([]CredentialVersion, error) {
	if err := s.ensureMigrated(ctx); err != nil {
		return nil, err
	}
	if err := validateName(name); err != nil {
		return nil, err
	}

	switch s.strategy {
	case StrategyManual:
		return s.listVersionsManual(name)
	case StrategyBarrier:
		return s.listVersionsBarrier(ctx, name)
	default:
		return nil, ErrInvalidStrategy
	}
}

// GCExpiredVersions removes any historical versions of name whose
// grace period has elapsed. Safe to call repeatedly; idempotent.
// Returns the number of versions reaped.
//
// Callers running a background sweep should iterate ListCredentials
// and invoke this per name on whatever cadence matches their grace
// granularity (typically once per minute).
func (s *CredentialService) GCExpiredVersions(ctx context.Context, name string) (int, error) {
	if err := s.ensureMigrated(ctx); err != nil {
		return 0, err
	}
	if err := validateName(name); err != nil {
		return 0, err
	}

	var reaped int
	var err error
	switch s.strategy {
	case StrategyManual:
		reaped = s.gcManual(name)
	case StrategyBarrier:
		reaped, err = s.gcBarrier(ctx, name)
	default:
		return 0, ErrInvalidStrategy
	}
	if err != nil {
		return reaped, err
	}

	if reaped > 0 {
		s.emitAudit(ctx, "gc", name, map[string]any{"reaped": reaped})
	}
	return reaped, nil
}

// =============================================================================
// Manual strategy impl
// =============================================================================

// manualVersionStore lazily extends manualCreds with versioned data.
// The CredentialService's existing manualCreds map keeps holding the
// current value; this map holds non-current grace-period values and
// per-name metadata.
//
// We attach it via an embedded struct so existing tests that inspect
// manualCreds continue to work unchanged.

func (s *CredentialService) ensureManualVersionStore() {
	if s.manualVersions == nil {
		s.manualVersions = make(map[string]map[uint64]*secure.Secret)
	}
	if s.manualMeta == nil {
		s.manualMeta = make(map[string]*credentialMeta)
	}
}

func (s *CredentialService) rotateManual(ctx context.Context, name string, opts RotateOptions) (*RotateResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ensureManualVersionStore()

	prevValue, prevExists := s.manualCreds[name]
	meta := s.manualMeta[name]
	if meta == nil {
		// Implicit v1 promotion: an existing untracked value is
		// version 1; an absent value means the rotation creates
		// version 1 outright.
		meta = &credentialMeta{}
		if prevExists {
			meta.CurrentVersion = 1
		}
	}

	prevVersion := meta.CurrentVersion
	newVersion := prevVersion + 1
	if !prevExists && prevVersion == 0 {
		newVersion = 1
	}

	now := time.Now().UTC()
	var graceUntil time.Time
	if prevExists && opts.GracePeriod > 0 {
		graceUntil = now.Add(opts.GracePeriod)
		if s.manualVersions[name] == nil {
			s.manualVersions[name] = make(map[uint64]*secure.Secret)
		}
		// Transfer ownership of the previous enclave to the grace-version
		// slot; no plaintext is copied through heap memory.
		s.manualVersions[name][prevVersion] = prevValue
		meta.History = append(meta.History, credentialMetaEntry{
			Version:    prevVersion,
			CreatedAt:  now, // best-effort; we don't track original creation for legacy v1
			GraceUntil: graceUntil,
		})
	} else if prevExists {
		// No-grace rotation: destroy the previous enclave immediately.
		prevValue.Destroy()
	}

	sec, err := newSecretCopy(ctx, opts.NewValue)
	if err != nil {
		return nil, err
	}
	s.manualCreds[name] = sec
	meta.CurrentVersion = newVersion
	s.manualMeta[name] = meta

	s.logger.Debug("credential rotated",
		slog.String("name", name),
		slog.String("strategy", StrategyManual),
		slog.Uint64("previous_version", prevVersion),
		slog.Uint64("new_version", newVersion),
		slog.Duration("grace_period", opts.GracePeriod))

	return &RotateResult{
		Name:            name,
		PreviousVersion: prevVersion,
		NewVersion:      newVersion,
		GraceUntil:      graceUntil,
	}, nil
}

func (s *CredentialService) retrieveManualVersion(ctx context.Context, name string, version uint64) (*secure.Buffer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	meta := s.manualMeta[name]
	if meta == nil {
		// Legacy untracked credential = v1 only.
		if version == 1 {
			if sec, ok := s.manualCreds[name]; ok {
				return sec.Open(ctx)
			}
			return nil, ErrCredentialNotFound
		}
		return nil, ErrVersionNotFound
	}

	if version == meta.CurrentVersion {
		if sec, ok := s.manualCreds[name]; ok {
			return sec.Open(ctx)
		}
		return nil, ErrCredentialNotFound
	}

	// Historical version — check grace status.
	now := time.Now().UTC()
	for _, h := range meta.History {
		if h.Version != version {
			continue
		}
		if !h.GraceUntil.IsZero() && now.After(h.GraceUntil) {
			return nil, ErrVersionExpired
		}
		if sec, ok := s.manualVersions[name][version]; ok {
			return sec.Open(ctx)
		}
		return nil, ErrVersionExpired
	}
	return nil, ErrVersionNotFound
}

func (s *CredentialService) listVersionsManual(name string) ([]CredentialVersion, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	_, exists := s.manualCreds[name]
	if !exists {
		return nil, ErrCredentialNotFound
	}
	meta := s.manualMeta[name]
	if meta == nil {
		// Legacy untracked credential — v1, current, no grace.
		return []CredentialVersion{{
			Name:      name,
			Version:   1,
			IsCurrent: true,
		}}, nil
	}
	out := make([]CredentialVersion, 0, 1+len(meta.History))
	out = append(out, CredentialVersion{
		Name:      name,
		Version:   meta.CurrentVersion,
		IsCurrent: true,
	})
	now := time.Now().UTC()
	for _, h := range meta.History {
		if !h.GraceUntil.IsZero() && now.After(h.GraceUntil) {
			continue
		}
		out = append(out, CredentialVersion{
			Name:       name,
			Version:    h.Version,
			CreatedAt:  h.CreatedAt,
			GraceUntil: h.GraceUntil,
		})
	}
	return out, nil
}

func (s *CredentialService) gcManual(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	meta := s.manualMeta[name]
	if meta == nil {
		return 0
	}
	now := time.Now().UTC()
	kept := make([]credentialMetaEntry, 0, len(meta.History))
	reaped := 0
	for _, h := range meta.History {
		if !h.GraceUntil.IsZero() && now.After(h.GraceUntil) {
			if sec, ok := s.manualVersions[name][h.Version]; ok {
				sec.Destroy()
				delete(s.manualVersions[name], h.Version)
			}
			reaped++
			continue
		}
		kept = append(kept, h)
	}
	meta.History = kept
	if len(s.manualVersions[name]) == 0 {
		delete(s.manualVersions, name)
	}
	return reaped
}

// =============================================================================
// Barrier strategy impl
// =============================================================================

func (s *CredentialService) listBarrier(ctx context.Context) ([]string, error) {
	keys, err := s.store.List(ctx, credentialPrefix)
	if err != nil {
		return nil, &OperationError{Op: "list barrier", Cause: err}
	}
	names := make([]string, 0, len(keys))
	for _, k := range keys {
		// Only values live under the prefix, so every key is a name.
		names = append(names, strings.TrimPrefix(k, credentialPrefix))
	}
	sortStrings(names)
	return names, nil
}

// rotateBarrier lands a rotation in one guarded batch: the retained copy of
// the previous version, the new current value and the metadata record are
// written together, and only while the metadata record and the current
// value are still exactly what the rotation read. A refused batch means
// another writer got there first; the rotation reads again and re-applies.
// A manual rotation then numbers its versions from the fresh record. A
// scheduled one goes on only while the record still carries its claim and
// still names its staged version, and otherwise returns
// ErrRotationClaimLost having written nothing.
func (s *CredentialService) rotateBarrier(ctx context.Context, name string, opts RotateOptions) (*RotateResult, error) {
	sched := opts.schedule
	if sched != nil && !sched.resumed {
		if err := s.stageScheduled(ctx, name, sched, opts.NewValue); err != nil {
			return nil, err
		}
	}
	for range rotationMetaRetries {
		meta, rawMeta, err := s.readBarrierMeta(ctx, name)
		if err != nil {
			return nil, err
		}
		prevValue, prevExists, err := s.loadBarrierCurrent(ctx, name)
		if err != nil {
			return nil, err
		}
		prevVersion, newVersion := rotationVersions(meta.CurrentVersion, prevExists)
		// A scheduled commit is fenced on its claim and its staged
		// version: when the claim is gone (the credential was deleted) or
		// replaced, or a rotation or a stored value since took the staged
		// version's place, nothing is written, so a deleted credential's
		// records are never recreated. Any landing write since also ended
		// the claim's operation, which the fence checks too.
		if sched != nil && (!prevExists || !sched.holds(meta) ||
			meta.Staged != sched.staged || newVersion != sched.staged) {
			secure.Wipe(prevValue)
			s.abandonScheduled(ctx, name, sched.staged)
			return nil, &OperationError{Op: "commit rotation", Cause: ErrRotationClaimLost}
		}

		now := time.Now().UTC()
		var graceUntil time.Time
		if opts.GracePeriod > 0 {
			graceUntil = now.Add(opts.GracePeriod)
		}
		guards := []kvstore.Guard{{Key: metaKey(name), Value: rawMeta}, {Key: credentialPrefix + name}}
		if prevExists {
			guards[1].Value = prevValue
		}
		var writes []kvstore.KV
		if prevExists && (opts.GracePeriod > 0 || opts.retainPrevious) {
			writes = append(writes, kvstore.KV{Key: versionKey(name, prevVersion), Value: prevValue})
			meta.History = append(meta.History, credentialMetaEntry{
				Version:    prevVersion,
				CreatedAt:  now,
				GraceUntil: graceUntil,
			})
		}
		// The version fields are applied to the record as it stands at
		// the read the batch is guarded on, so a policy change or claim
		// made before it survives. A scheduled rotation settles its claim
		// and schedule in the same write.
		meta.CurrentVersion = newVersion
		meta.Staged = 0
		// Any landed version ends the pending scheduled rotation's
		// operation: the next one must not recover a value issued for a
		// rotation this version superseded.
		meta.RotationOp = ""
		if sched != nil {
			sched.settle(meta)
		}
		nextMeta, err := json.Marshal(meta)
		if err != nil {
			secure.Wipe(prevValue)
			return nil, &OperationError{Op: "encode meta", Cause: err}
		}
		// newVersion becomes current, so a value staged for it is spent,
		// whichever rotation staged it. It is deleted in the guarded batch:
		// a delete after the commit could remove a value a recreated
		// credential has since staged under the same version.
		writes = append(writes,
			kvstore.KV{Key: credentialPrefix + name, Value: opts.NewValue},
			kvstore.KV{Key: metaKey(name), Value: nextMeta},
			kvstore.KV{Key: stagedKey(name, newVersion), Delete: true})
		// An import writes its copy's record in the same batch, guarded
		// absent: a service derived for another request shares no lock
		// with this one, so only this guard refuses a second commit of a
		// copy another import committed since the caller's own check.
		if opts.importDigest != "" {
			k := importKey(name, opts.importDigest)
			guards = append(guards, kvstore.Guard{Key: k})
			writes = append(writes, kvstore.KV{Key: k, Value: []byte(strconv.FormatUint(newVersion, 10))})
		}
		applied, err := s.store.PutAllIfAllUnchanged(ctx, guards, writes)
		// The in-memory copy of the previous value is wiped once it is on
		// disk, or once this attempt is over.
		secure.Wipe(prevValue)
		if err != nil {
			return nil, &OperationError{Op: "commit rotation", Cause: err}
		}
		if !applied {
			if opts.importDigest != "" {
				done, err := s.getOptional(ctx, importKey(name, opts.importDigest))
				if err != nil {
					return nil, &OperationError{Op: "read migration import", Cause: err}
				}
				if done != nil {
					return nil, errAlreadyImported
				}
			}
			continue
		}
		s.logger.Debug("credential rotated",
			slog.String("name", name),
			slog.String("strategy", StrategyBarrier),
			slog.Uint64("previous_version", prevVersion),
			slog.Uint64("new_version", newVersion),
			slog.Duration("grace_period", opts.GracePeriod))

		return &RotateResult{
			Name:            name,
			PreviousVersion: prevVersion,
			NewVersion:      newVersion,
			GraceUntil:      graceUntil,
		}, nil
	}
	return nil, &OperationError{Op: "commit rotation", Cause: ErrRotationMetaContended}
}

// stageScheduled persists a scheduled rotation's new value before it
// becomes current: the staged value and the metadata record naming its
// version are written in one batch, guarded on the metadata as read. The
// staged value is the evidence a later attempt resumes from instead of
// invoking the value source again, and only the version the metadata names
// is ever resumed. It is keyed apart from the version records, so the
// delete that spends it cannot remove a version a rotation retained. A
// claim that is gone or replaced stages nothing.
func (s *CredentialService) stageScheduled(ctx context.Context, name string, sched *scheduledRotation, value []byte) error {
	for range rotationMetaRetries {
		meta, rawMeta, err := s.readBarrierMeta(ctx, name)
		if err != nil {
			return err
		}
		// The claim alone does not fence a replacement: a stored value or
		// manual rotation that landed while the source ran keeps the claim
		// but ends the operation the source ran for, so its value is stale.
		if !sched.holds(meta) {
			return &OperationError{Op: "stage rotation", Cause: ErrRotationClaimLost}
		}
		current, exists, err := s.loadBarrierCurrent(ctx, name)
		if err != nil {
			return err
		}
		secure.Wipe(current)
		_, next := rotationVersions(meta.CurrentVersion, exists)
		meta.Staged = next
		nextMeta, err := json.Marshal(meta)
		if err != nil {
			return &OperationError{Op: "encode meta", Cause: err}
		}
		applied, err := s.store.PutAllIfAllUnchanged(ctx,
			[]kvstore.Guard{{Key: metaKey(name), Value: rawMeta}},
			[]kvstore.KV{{Key: stagedKey(name, next), Value: value}, {Key: metaKey(name), Value: nextMeta}})
		if err != nil {
			return &OperationError{Op: "stage rotation", Cause: err}
		}
		if applied {
			sched.staged = next
			return nil
		}
	}
	return &OperationError{Op: "stage rotation", Cause: ErrRotationMetaContended}
}

// storeBarrier writes the credential's current value in one batch guarded
// on the metadata record as read. A record naming a pending scheduled
// rotation, by its operation ID or by a staged value, is written in the same
// batch with both cleared, and the staged value is deleted by it: the stored
// value supersedes that rotation, so the next scheduled rotation starts a
// new operation, invokes its value source again and retains the stored
// value, and an idempotent source cannot replay the superseded operation's
// value over it. The current version is not advanced.
func (s *CredentialService) storeBarrier(ctx context.Context, name string, value []byte) error {
	for range rotationMetaRetries {
		meta, rawMeta, err := s.readBarrierMeta(ctx, name)
		if err != nil {
			return err
		}
		writes := []kvstore.KV{{Key: credentialPrefix + name, Value: value}}
		if staged := meta.Staged; staged != 0 || meta.RotationOp != "" {
			meta.Staged = 0
			meta.RotationOp = ""
			nextMeta, err := json.Marshal(meta)
			if err != nil {
				return &OperationError{Op: "encode meta", Cause: err}
			}
			writes = append(writes, kvstore.KV{Key: metaKey(name), Value: nextMeta})
			if staged != 0 {
				writes = append(writes, kvstore.KV{Key: stagedKey(name, staged), Delete: true})
			}
		}
		applied, err := s.store.PutAllIfAllUnchanged(ctx, []kvstore.Guard{{Key: metaKey(name), Value: rawMeta}}, writes)
		if err != nil {
			return err
		}
		if applied {
			return nil
		}
	}
	return &OperationError{Op: "store credential", Cause: ErrRotationMetaContended}
}

// abandonScheduled removes the value staged for version by a scheduled
// rotation that will not commit it, unless the metadata record still names
// it. A staged value the record does not name is never resumed; one left by
// a failed delete is reaped by the scheduled cleanup or the credential's
// delete.
func (s *CredentialService) abandonScheduled(ctx context.Context, name string, version uint64) {
	// The delete is guarded on the metadata read that found the version
	// unnamed: a credential deleted and recreated since may have staged
	// the same version, and its staged value must survive.
	for range rotationMetaRetries {
		meta, rawMeta, err := s.readBarrierMeta(ctx, name)
		if err != nil {
			s.logger.Warn("abandoned staged rotation value left behind",
				slog.String("name", name),
				slog.Uint64("version", version),
				slog.String("error", err.Error()))
			return
		}
		if meta.Staged == version {
			return
		}
		applied, err := s.store.PutAllIfAllUnchanged(ctx,
			[]kvstore.Guard{{Key: metaKey(name), Value: rawMeta}},
			[]kvstore.KV{{Key: stagedKey(name, version), Delete: true}})
		if err != nil {
			s.logger.Warn("abandoned staged rotation value left behind",
				slog.String("name", name),
				slog.Uint64("version", version),
				slog.String("error", err.Error()))
			return
		}
		if applied {
			return
		}
	}
	s.logger.Warn("abandoned staged rotation value left behind",
		slog.String("name", name),
		slog.Uint64("version", version),
		slog.String("error", ErrRotationMetaContended.Error()))
}

// rotationVersions returns the version a rotation retires and the one it
// writes, given the record's current version and whether a current value
// exists. An existing untracked value is v1.
func rotationVersions(current uint64, exists bool) (prev, next uint64) {
	prev = current
	if exists && prev == 0 {
		prev = 1
	}
	if !exists && prev == 0 {
		return 0, 1
	}
	return prev, prev + 1
}

func (s *CredentialService) retrieveBarrierVersion(ctx context.Context, name string, version uint64) (*secure.Buffer, error) {
	meta, err := s.loadBarrierMeta(ctx, name)
	if err != nil {
		return nil, err
	}

	if meta.CurrentVersion == 0 {
		// Legacy untracked v1.
		if version != 1 {
			return nil, ErrVersionNotFound
		}
		value, err := s.store.Get(ctx, credentialPrefix+name)
		if err != nil {
			return nil, readFailure("retrieve version", err, ErrCredentialNotFound)
		}
		return bufferFromHeap(value)
	}

	if version == meta.CurrentVersion {
		value, err := s.store.Get(ctx, credentialPrefix+name)
		if err != nil {
			return nil, readFailure("retrieve version", err, ErrCredentialNotFound)
		}
		return bufferFromHeap(value)
	}
	now := time.Now().UTC()
	for _, h := range meta.History {
		if h.Version != version {
			continue
		}
		if !h.GraceUntil.IsZero() && now.After(h.GraceUntil) {
			return nil, ErrVersionExpired
		}
		value, err := s.store.Get(ctx, versionKey(name, version))
		if err != nil {
			return nil, readFailure("retrieve version", err, ErrVersionExpired)
		}
		return bufferFromHeap(value)
	}
	return nil, ErrVersionNotFound
}

func (s *CredentialService) listVersionsBarrier(ctx context.Context, name string) ([]CredentialVersion, error) {
	exists, err := s.barrierExists(ctx, name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrCredentialNotFound
	}
	meta, err := s.loadBarrierMeta(ctx, name)
	if err != nil {
		return nil, err
	}
	if meta.CurrentVersion == 0 {
		return []CredentialVersion{{
			Name:      name,
			Version:   1,
			IsCurrent: true,
		}}, nil
	}
	out := make([]CredentialVersion, 0, 1+len(meta.History))
	out = append(out, CredentialVersion{
		Name:      name,
		Version:   meta.CurrentVersion,
		IsCurrent: true,
	})
	now := time.Now().UTC()
	for _, h := range meta.History {
		if !h.GraceUntil.IsZero() && now.After(h.GraceUntil) {
			continue
		}
		out = append(out, CredentialVersion{
			Name:       name,
			Version:    h.Version,
			CreatedAt:  h.CreatedAt,
			GraceUntil: h.GraceUntil,
		})
	}
	return out, nil
}

func (s *CredentialService) gcBarrier(ctx context.Context, name string) (int, error) {
	now := time.Now().UTC()
	return s.reapBarrierVersions(ctx, name, func(meta *credentialMeta) ([]credentialMetaEntry, bool) {
		expired := make([]credentialMetaEntry, 0, len(meta.History))
		for _, h := range meta.History {
			if !h.GraceUntil.IsZero() && now.After(h.GraceUntil) {
				expired = append(expired, h)
			}
		}
		return expired, true
	})
}

// reapBarrierVersions deletes the versions pick selects from the metadata
// record and removes them from its history, in one batch guarded on the
// record pick read: a rotation, policy change, or delete and recreate
// between the read and the write refuses the batch, nothing is deleted, and
// the record is read and picked from again. pick returning false writes
// nothing. Otherwise the record is written even when nothing is reaped, so a
// sealed barrier fails the call closed.
func (s *CredentialService) reapBarrierVersions(ctx context.Context, name string, pick func(*credentialMeta) ([]credentialMetaEntry, bool)) (int, error) {
	for range rotationMetaRetries {
		meta, rawMeta, err := s.readBarrierMeta(ctx, name)
		if err != nil {
			return 0, err
		}
		versions, ok := pick(meta)
		if !ok {
			return 0, nil
		}
		gone := make(map[uint64]struct{}, len(versions))
		writes := make([]kvstore.KV, 0, len(versions)+1)
		for _, h := range versions {
			gone[h.Version] = struct{}{}
			writes = append(writes, kvstore.KV{Key: versionKey(name, h.Version), Delete: true})
		}
		kept := make([]credentialMetaEntry, 0, len(meta.History))
		for _, h := range meta.History {
			if _, ok := gone[h.Version]; !ok {
				kept = append(kept, h)
			}
		}
		meta.History = kept
		nextMeta, err := json.Marshal(meta)
		if err != nil {
			return 0, &OperationError{Op: "encode meta", Cause: err}
		}
		writes = append(writes, kvstore.KV{Key: metaKey(name), Value: nextMeta})
		applied, err := s.store.PutAllIfAllUnchanged(ctx, []kvstore.Guard{{Key: metaKey(name), Value: rawMeta}}, writes)
		if err != nil {
			return 0, &OperationError{Op: "write meta", Cause: err}
		}
		if applied {
			return len(versions), nil
		}
	}
	return 0, &OperationError{Op: "write meta", Cause: ErrRotationMetaContended}
}

// deleteBarrierAll removes a barrier-strategy credential's current value,
// its metadata sidecar, and every historical version (grace-period or not)
// so that a deleted credential's previous values are no longer retrievable
// through RetrieveCredentialVersion. History, staged values, metadata (which
// holds the rotation policy) and the current value go in one commit guarded
// on the metadata and the value as read, so a failure deletes nothing and a
// SetRotationPolicy or rotation landing between the read and the commit
// fails the guard: the delete then selects again.
func (s *CredentialService) deleteBarrierAll(ctx context.Context, name string) error {
	metaK, valueK := metaKey(name), credentialPrefix+name
	stagedPrefix := StagedKeyPrefix + encodedName(name) + "/"
	for range rotationMetaRetries {
		// The selection is read from the metadata and the batch is
		// guarded on it: a rotation that retains a version, or stages
		// one, rewrites the metadata in the same batch, so one landing
		// between the selection and the delete fails the guard and the
		// whole selection is taken again. Deleting history before the
		// head in separate steps let such a rotation orphan its retained
		// value with no metadata naming it.
		meta, rawMeta, err := s.readBarrierMeta(ctx, name)
		if err != nil {
			return err
		}
		rawValue, err := s.getOptional(ctx, valueK)
		if err != nil {
			return &OperationError{Op: "read current", Cause: err}
		}
		staged, err := s.store.List(ctx, stagedPrefix)
		if err != nil {
			secure.Wipe(rawValue)
			return &OperationError{Op: "list staged rotations", Cause: err}
		}
		writes := make([]kvstore.KV, 0, len(meta.History)+len(staged)+2)
		for _, h := range meta.History {
			writes = append(writes, kvstore.KV{Key: versionKey(name, h.Version), Delete: true})
		}
		// Every value a scheduled rotation staged under the name goes
		// too, an unfinished rotation's and a spent one a failed cleanup
		// left alike, so a credential later stored under the name never
		// lands one.
		for _, k := range staged {
			version, err := strconv.ParseUint(strings.TrimPrefix(k, stagedPrefix), 10, 64)
			if err != nil || k != stagedKey(name, version) {
				continue
			}
			writes = append(writes, kvstore.KV{Key: k, Delete: true})
		}
		// A legacy migration interrupted before its install still holds a
		// pending claim on the name; the delete settles it in the same
		// batch, so the install, retried after the delete, restores none
		// of the deleted value, history or policy.
		rawClaim, fence, err := s.deletionFence(ctx, name)
		if err != nil {
			secure.Wipe(rawValue)
			return err
		}
		// A name with nothing to delete and no pending claim gets no
		// tombstone: there is no deleted value a migration could restore.
		if rawMeta == nil && rawValue == nil && len(writes) == 0 && (fence == nil || rawClaim == nil) {
			return nil
		}
		writes = append(writes, kvstore.KV{Key: metaK, Delete: true}, kvstore.KV{Key: valueK, Delete: true})
		// The claim is guarded whether or not it is rewritten (a nil
		// value holds only while it is absent): a claim written since the
		// read fails the batch and the selection is taken again, so it is
		// fenced too. A name with no claim gets a deletion tombstone,
		// which settles a pre-upgrade copy another node migrates later.
		guards := []kvstore.Guard{{Key: metaK, Value: rawMeta}, {Key: valueK, Value: rawValue},
			{Key: MigrationClaimPrefix + name, Value: rawClaim}}
		if fence != nil {
			writes = append(writes, kvstore.KV{Key: MigrationClaimPrefix + name, Value: fence})
		}
		applied, err := s.store.PutAllIfAllUnchanged(ctx, guards, writes)
		secure.Wipe(rawValue)
		if err != nil {
			return &OperationError{Op: "delete current", Cause: err}
		}
		if applied {
			return nil
		}
	}
	return &OperationError{Op: "delete current", Cause: ErrRotationMetaContended}
}

// getOptional reads key, returning nil with a nil error when it is absent.
// A present empty value is returned as a non-nil empty slice, so a guard
// built from it still requires the key to be present.
func (s *CredentialService) getOptional(ctx context.Context, key string) ([]byte, error) {
	raw, err := s.store.Get(ctx, key)
	switch {
	case err == nil:
		if raw == nil {
			raw = []byte{}
		}
		return raw, nil
	case errors.Is(err, seal.ErrSecretNotFound):
		return nil, nil
	default:
		return nil, err
	}
}

// =============================================================================
// Barrier metadata helpers
// =============================================================================

// encodedName is name as the version and staged keys carry it: unpadded
// URL-safe base64, which holds no '/'.
func encodedName(name string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(name))
}

func versionKey(name string, version uint64) string {
	return VersionKeyPrefix + encodedName(name) + "/" + strconv.FormatUint(version, 10)
}

// stagedKey keys a scheduled rotation's staged value apart from the version
// records, so removing a spent staged value can never remove a retained
// version that a concurrent rotation wrote under the same number.
func stagedKey(name string, version uint64) string {
	return StagedKeyPrefix + encodedName(name) + "/" + strconv.FormatUint(version, 10)
}

func metaKey(name string) string {
	return MetaKeyPrefix + name
}

func (s *CredentialService) loadBarrierMeta(ctx context.Context, name string) (*credentialMeta, error) {
	meta, _, err := s.readBarrierMeta(ctx, name)
	return meta, err
}

// readBarrierMeta returns the metadata record with the bytes it was decoded
// from, the guard of a batch that writes it. The bytes are nil when the
// record is absent.
func (s *CredentialService) readBarrierMeta(ctx context.Context, name string) (*credentialMeta, []byte, error) {
	raw, err := s.store.Get(ctx, metaKey(name))
	if errors.Is(err, seal.ErrSecretNotFound) {
		// Missing metadata is normal for legacy (untracked) entries
		// and for credentials that have never been rotated. Return
		// a zero-valued meta the caller can populate.
		return &credentialMeta{}, nil, nil
	}
	if err != nil {
		// Any other failure is not absence: a caller deciding on an
		// empty record would number a version, or invoke the rotation
		// value source, over metadata it never read.
		return nil, nil, &OperationError{Op: "read meta", Cause: err}
	}
	var meta credentialMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, nil, &OperationError{Op: "decode meta", Cause: err}
	}
	return &meta, raw, nil
}

func (s *CredentialService) loadBarrierCurrent(ctx context.Context, name string) ([]byte, bool, error) {
	value, err := s.store.Get(ctx, credentialPrefix+name)
	if errors.Is(err, seal.ErrSecretNotFound) {
		return nil, false, nil
	}
	if err != nil {
		// Any other failure is not absence: a caller deciding on absence
		// would number a version, or invoke the rotation value source,
		// over a value it never read.
		return nil, false, &OperationError{Op: "read current", Cause: err}
	}
	return value, true, nil
}

// readFailure maps the error of a store read to what the caller is told:
// absent when the store answered that the key does not exist, and an
// OperationError for every other failure, which says nothing about the key.
func readFailure(op string, err, absent error) error {
	if errors.Is(err, seal.ErrSecretNotFound) {
		return absent
	}
	return &OperationError{Op: op, Cause: err}
}

// barrierExists reports whether the credential has a current value. The
// value is read to learn that and wiped before returning.
func (s *CredentialService) barrierExists(ctx context.Context, name string) (bool, error) {
	value, exists, err := s.loadBarrierCurrent(ctx, name)
	secure.Wipe(value)
	return exists, err
}

// =============================================================================
// Tiny utility
// =============================================================================

// sortStrings is a thin wrapper over sort.Strings to keep an import
// budget under control in the versioned file.
func sortStrings(in []string) {
	// Pure insertion sort — credential lists are O(tens), not O(thousands).
	for i := 1; i < len(in); i++ {
		j := i
		for j > 0 && in[j-1] > in[j] {
			in[j-1], in[j] = in[j], in[j-1]
			j--
		}
	}
}

// deleteBarrierStaged removes the staged rotation values of exactly the
// named credential that were staged for a version up to upTo.
func (s *CredentialService) deleteBarrierStaged(ctx context.Context, name string, upTo uint64) error {
	prefix := StagedKeyPrefix + encodedName(name) + "/"
	keys, err := s.store.List(ctx, prefix)
	if err != nil {
		return &OperationError{Op: "list staged rotations", Cause: err}
	}
	for _, k := range keys {
		// A staged key ends in its version.
		version, err := strconv.ParseUint(strings.TrimPrefix(k, prefix), 10, 64)
		if err != nil || k != stagedKey(name, version) || version > upTo {
			continue
		}
		if err := s.store.Delete(ctx, k); err != nil {
			return &OperationError{Op: "delete staged rotation", Cause: err}
		}
	}
	return nil
}

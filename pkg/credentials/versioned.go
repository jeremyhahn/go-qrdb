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
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// =============================================================================
// Storage layout
// =============================================================================
//
// A versioned credential at logical name "<name>" is laid out as:
//
//	credentials/<name>           — the current value (always the latest)
//	credentials/<name>#meta      — JSON metadata (current version + grace history)
//	credentials/<name>#v<N>      — historical value for version N (kept while
//	                                in grace period; deleted by GC after expiry)
//
// The plain "credentials/<name>" entry is the existing key the
// non-versioned API has always used. Credentials originally written
// before rotation existed look like "<name> at v1 with no recorded
// metadata"; the first Rotate call promotes them by writing #meta.

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

// credentialMeta is the on-disk JSON layout for the #meta sidecar.
type credentialMeta struct {
	// CurrentVersion is the monotonic version of the value at
	// credentials/<name>.
	CurrentVersion uint64 `json:"current_version"`

	// History records non-current versions that have been written.
	// Entries are pruned when their grace period elapses.
	History []credentialMetaEntry `json:"history,omitempty"`
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
	if err := validateName(name); err != nil {
		return nil, err
	}
	if err := validateValue(opts.NewValue); err != nil {
		return nil, err
	}
	if opts.GracePeriod < 0 {
		return nil, ErrInvalidGracePeriod
	}

	switch s.strategy {
	case StrategyManual:
		return s.rotateManual(name, opts)
	case StrategyBarrier:
		return s.rotateBarrier(ctx, name, opts)
	default:
		return nil, ErrInvalidStrategy
	}
}

// RetrieveCredentialVersion returns the stored credential value for a
// specific version. Use version == 0 to mean "current version" (same
// as RetrieveCredential).
//
// Returns ErrVersionNotFound when the version was never written and
// ErrVersionExpired when the version was once present but its grace
// period has elapsed (GC may or may not have reaped the storage yet —
// either way the caller is denied).
func (s *CredentialService) RetrieveCredentialVersion(ctx context.Context, name string, version uint64) ([]byte, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}

	if version == 0 {
		return s.RetrieveCredential(ctx, name)
	}

	switch s.strategy {
	case StrategyManual:
		return s.retrieveManualVersion(name, version)
	case StrategyBarrier:
		return s.retrieveBarrierVersion(ctx, name, version)
	default:
		return nil, ErrInvalidStrategy
	}
}

// ListCredentialVersions returns the version history (current + all
// non-expired grace versions) for name. Each entry's Value field is
// nil — use RetrieveCredentialVersion to read a specific value.
//
// Returns ErrCredentialNotFound when name does not exist.
func (s *CredentialService) ListCredentialVersions(ctx context.Context, name string) ([]CredentialVersion, error) {
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
	if err := validateName(name); err != nil {
		return 0, err
	}

	switch s.strategy {
	case StrategyManual:
		return s.gcManual(name), nil
	case StrategyBarrier:
		return s.gcBarrier(ctx, name)
	default:
		return 0, ErrInvalidStrategy
	}
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

func (s *CredentialService) rotateManual(name string, opts RotateOptions) (*RotateResult, error) {
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

	sec, err := newSecretCopy(opts.NewValue)
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

func (s *CredentialService) retrieveManualVersion(name string, version uint64) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	meta := s.manualMeta[name]
	if meta == nil {
		// Legacy untracked credential = v1 only.
		if version == 1 {
			if sec, ok := s.manualCreds[name]; ok {
				return openSecretCopy(sec)
			}
			return nil, ErrCredentialNotFound
		}
		return nil, ErrVersionNotFound
	}

	if version == meta.CurrentVersion {
		if sec, ok := s.manualCreds[name]; ok {
			return openSecretCopy(sec)
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
			return openSecretCopy(sec)
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
	keys, err := s.barrier.List(ctx, credentialPrefix)
	if err != nil {
		return nil, &OperationError{Op: "list barrier", Cause: err}
	}
	names := make([]string, 0, len(keys))
	for _, k := range keys {
		name := strings.TrimPrefix(k, credentialPrefix)
		// Skip metadata + historical-version sidecars; only
		// surface logical names.
		if strings.Contains(name, "#") {
			continue
		}
		names = append(names, name)
	}
	sortStrings(names)
	return names, nil
}

func (s *CredentialService) rotateBarrier(ctx context.Context, name string, opts RotateOptions) (*RotateResult, error) {
	meta, err := s.loadBarrierMeta(ctx, name)
	if err != nil {
		return nil, err
	}

	prevValue, prevExists, err := s.loadBarrierCurrent(ctx, name)
	if err != nil {
		return nil, err
	}
	prevVersion := meta.CurrentVersion
	if prevExists && prevVersion == 0 {
		// Legacy promotion: existing untracked value is v1.
		prevVersion = 1
		meta.CurrentVersion = 1
	}
	newVersion := prevVersion + 1
	if !prevExists && prevVersion == 0 {
		newVersion = 1
	}

	now := time.Now().UTC()
	var graceUntil time.Time
	if prevExists && opts.GracePeriod > 0 {
		graceUntil = now.Add(opts.GracePeriod)
		if err := s.barrier.Put(ctx, versionKey(name, prevVersion), prevValue); err != nil {
			return nil, &OperationError{Op: "write grace version", Cause: err}
		}
		meta.History = append(meta.History, credentialMetaEntry{
			Version:    prevVersion,
			CreatedAt:  now,
			GraceUntil: graceUntil,
		})
	}

	if err := s.barrier.Put(ctx, credentialPrefix+name, opts.NewValue); err != nil {
		return nil, &OperationError{Op: "write current", Cause: err}
	}
	meta.CurrentVersion = newVersion
	if err := s.writeBarrierMeta(ctx, name, meta); err != nil {
		return nil, err
	}

	// Wipe in-memory copy of the previous value once it is on disk
	// (or once we've decided not to keep it).
	if prevExists {
		secure.Wipe(prevValue)
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

func (s *CredentialService) retrieveBarrierVersion(ctx context.Context, name string, version uint64) ([]byte, error) {
	meta, err := s.loadBarrierMeta(ctx, name)
	if err != nil {
		return nil, err
	}

	if meta.CurrentVersion == 0 {
		// Legacy untracked v1.
		if version != 1 {
			return nil, ErrVersionNotFound
		}
		value, err := s.barrier.Get(ctx, credentialPrefix+name)
		if err != nil {
			return nil, ErrCredentialNotFound
		}
		return value, nil
	}

	if version == meta.CurrentVersion {
		value, err := s.barrier.Get(ctx, credentialPrefix+name)
		if err != nil {
			return nil, ErrCredentialNotFound
		}
		return value, nil
	}
	now := time.Now().UTC()
	for _, h := range meta.History {
		if h.Version != version {
			continue
		}
		if !h.GraceUntil.IsZero() && now.After(h.GraceUntil) {
			return nil, ErrVersionExpired
		}
		value, err := s.barrier.Get(ctx, versionKey(name, version))
		if err != nil {
			return nil, ErrVersionExpired
		}
		return value, nil
	}
	return nil, ErrVersionNotFound
}

func (s *CredentialService) listVersionsBarrier(ctx context.Context, name string) ([]CredentialVersion, error) {
	_, exists, err := s.loadBarrierCurrent(ctx, name)
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
	meta, err := s.loadBarrierMeta(ctx, name)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	kept := make([]credentialMetaEntry, 0, len(meta.History))
	reaped := 0
	for _, h := range meta.History {
		if !h.GraceUntil.IsZero() && now.After(h.GraceUntil) {
			if delErr := s.barrier.Delete(ctx, versionKey(name, h.Version)); delErr != nil {
				return reaped, &OperationError{Op: fmt.Sprintf("gc delete v%d", h.Version), Cause: delErr}
			}
			reaped++
			continue
		}
		kept = append(kept, h)
	}
	meta.History = kept
	if err := s.writeBarrierMeta(ctx, name, meta); err != nil {
		return reaped, err
	}
	return reaped, nil
}

// =============================================================================
// Barrier metadata helpers
// =============================================================================

func versionKey(name string, version uint64) string {
	return credentialPrefix + name + versionPrefix + strconv.FormatUint(version, 10)
}

func metaKey(name string) string {
	return credentialPrefix + name + metaSuffix
}

func (s *CredentialService) loadBarrierMeta(ctx context.Context, name string) (*credentialMeta, error) {
	raw, err := s.barrier.Get(ctx, metaKey(name))
	if err != nil {
		// Missing metadata is normal for legacy (untracked) entries
		// and for credentials that have never been rotated. Return
		// a zero-valued meta the caller can populate.
		return &credentialMeta{}, nil
	}
	var meta credentialMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, &OperationError{Op: "decode meta", Cause: err}
	}
	return &meta, nil
}

func (s *CredentialService) writeBarrierMeta(ctx context.Context, name string, meta *credentialMeta) error {
	raw, err := json.Marshal(meta)
	if err != nil {
		return &OperationError{Op: "encode meta", Cause: err}
	}
	if err := s.barrier.Put(ctx, metaKey(name), raw); err != nil {
		return &OperationError{Op: "write meta", Cause: err}
	}
	return nil
}

func (s *CredentialService) loadBarrierCurrent(ctx context.Context, name string) ([]byte, bool, error) {
	value, err := s.barrier.Get(ctx, credentialPrefix+name)
	if err != nil {
		// We can't distinguish "missing" from "I/O failure" at this
		// layer because StorageBarrier collapses everything to a
		// single error. Treat any error as "not found"; downstream
		// puts will fail if the I/O is genuinely broken.
		return nil, false, nil
	}
	return value, true, nil
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

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
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// officerRegistryPrefix is the storage key prefix under which persistent
// officer enrollment records are stored. The barrier transparently
// encrypts every value written under this prefix.
const officerRegistryPrefix = "ceremony/officers/"

// PersistentOfficer is the public, stable record persisted for each
// enrolled officer. It captures both the trust material (certificate
// thumbprint or public key) and the enrollment metadata (share index,
// auth factor, timestamps) needed to re-hydrate the in-memory
// state after a barrier unseal.
type PersistentOfficer struct {
	// Username is the unique identifier for the officer.
	Username string `json:"username"`

	// CertThumbprint is the lowercase hex-encoded SHA-256 hash of the
	// officer's X.509 certificate's DER bytes, when the officer was
	// enrolled with a certificate. Empty when only PubKeyPEM was provided.
	CertThumbprint string `json:"cert_thumbprint,omitempty"`

	// PubKeyPEM is the SPKI-encoded public key PEM. It is always present
	// (extracted from the certificate when CertThumbprint is set, or
	// supplied directly for pubkey-only enrollment).
	PubKeyPEM string `json:"pubkey_pem"`

	// ShareIndex is the 1-based Shamir share index assigned to this officer.
	ShareIndex int `json:"share_index"`

	// AuthFactor is the auth factor type the officer authenticates with
	// ("mtls", "webauthn", "piv").
	AuthFactor string `json:"auth_factor"`

	// EnrolledAt is the UTC timestamp when the officer was enrolled.
	EnrolledAt time.Time `json:"enrolled_at"`

	// ClaimedAt is the UTC timestamp when the officer claimed their
	// share. Zero value when not yet claimed.
	ClaimedAt time.Time `json:"claimed_at,omitempty"`
}

// OfficerRegistry persists officer enrollment records behind the data-plane
// storage barrier. All writes go through a seal.StorageBackend, which
// encrypts values transparently. Reads return ErrSealed when the barrier
// is sealed.
//
// The registry is concurrent-safe: a single sync.RWMutex guards an
// in-memory cache that is hydrated from the backend via Reload. Save,
// Load, ListAll, and MarkClaimed update both the cache and the backend
// atomically under the write lock.
type OfficerRegistry struct {
	mu      sync.RWMutex
	backend seal.StorageBackend
	cache   map[string]*PersistentOfficer
	// prefix is the storage key prefix under which records are persisted.
	// Defaults to officerRegistryPrefix for the system barrier; scoped
	// registries (tenant, recovery) use BarrierScope.StoragePrefix().
	prefix string
}

// NewOfficerRegistry creates a new OfficerRegistry backed by the given
// storage backend using the default system-barrier prefix. The backend MUST
// be a *seal.StorageBarrier (or any other backend that enforces sealed-state
// semantics) so that records are encrypted at rest and unavailable while sealed.
//
// Returns ErrNilStore if backend is nil.
func NewOfficerRegistry(backend seal.StorageBackend) (*OfficerRegistry, error) {
	if backend == nil {
		return nil, ErrNilStore
	}
	return &OfficerRegistry{
		backend: backend,
		cache:   make(map[string]*PersistentOfficer),
		prefix:  officerRegistryPrefix,
	}, nil
}

// NewScopedOfficerRegistry creates an OfficerRegistry scoped to the given
// BarrierScope. The scope determines the storage prefix and therefore the
// namespace isolation between system, tenant, and recovery officer sets.
//
// Returns ErrNilStore if backend is nil.
func NewScopedOfficerRegistry(backend seal.StorageBackend, scope BarrierScope) (*OfficerRegistry, error) {
	if backend == nil {
		return nil, ErrNilStore
	}
	return &OfficerRegistry{
		backend: backend,
		cache:   make(map[string]*PersistentOfficer),
		prefix:  scope.StoragePrefix(),
	}, nil
}

// Save persists the officer record. If a record with the same Username
// already exists, it is overwritten. If EnrolledAt is the zero value,
// the current UTC time is recorded.
//
// Returns ErrSealed if the underlying barrier is sealed; the error is
// surfaced verbatim from the backend.
func (r *OfficerRegistry) Save(ctx context.Context, officer *PersistentOfficer) error {
	if officer == nil {
		return ErrInvalidOfficerRecord
	}
	if officer.Username == "" {
		return ErrMissingUsername
	}

	// Normalize timestamp.
	if officer.EnrolledAt.IsZero() {
		officer.EnrolledAt = time.Now().UTC()
	} else {
		officer.EnrolledAt = officer.EnrolledAt.UTC()
	}
	if !officer.ClaimedAt.IsZero() {
		officer.ClaimedAt = officer.ClaimedAt.UTC()
	}

	blob, err := json.Marshal(officer)
	if err != nil {
		return &OfficerRegistryError{Op: "marshal", Err: err}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if err := r.backend.Put(ctx, r.officerKey(officer.Username), blob); err != nil {
		return err
	}

	// Cache a defensive copy so callers cannot mutate the shared instance.
	cp := *officer
	r.cache[officer.Username] = &cp
	return nil
}

// Load retrieves the officer record for the given username. The returned
// pointer references a fresh copy; callers may mutate it safely.
//
// Returns ErrUserNotFound if no record exists, or ErrSealed if the
// underlying barrier is sealed.
func (r *OfficerRegistry) Load(ctx context.Context, username string) (*PersistentOfficer, error) {
	if username == "" {
		return nil, ErrMissingUsername
	}

	// Fast-path: cache hit.
	r.mu.RLock()
	if cached, ok := r.cache[username]; ok {
		cp := *cached
		r.mu.RUnlock()
		return &cp, nil
	}
	r.mu.RUnlock()

	// Fall through to backend.
	blob, err := r.backend.Get(ctx, r.officerKey(username))
	if err != nil {
		if errors.Is(err, seal.ErrSecretNotFound) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}

	var officer PersistentOfficer
	if err := json.Unmarshal(blob, &officer); err != nil {
		return nil, &OfficerRegistryError{Op: "unmarshal", Err: err}
	}

	// Repopulate the cache opportunistically.
	r.mu.Lock()
	cp := officer
	r.cache[username] = &cp
	r.mu.Unlock()

	return &officer, nil
}

// ListAll returns every persistent officer record currently in storage,
// sorted by enrollment timestamp (oldest first; ties broken by username).
// Returns ErrSealed if the underlying barrier is sealed.
//
// The list is always served from the backend so that callers see the
// authoritative state — the cache is updated as a side effect.
func (r *OfficerRegistry) ListAll(ctx context.Context) ([]*PersistentOfficer, error) {
	keys, err := r.backend.List(ctx, r.prefix)
	if err != nil {
		return nil, err
	}

	out := make([]*PersistentOfficer, 0, len(keys))
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, k := range keys {
		username := strings.TrimPrefix(k, r.prefix)
		blob, getErr := r.backend.Get(ctx, k)
		if getErr != nil {
			return nil, getErr
		}
		var officer PersistentOfficer
		if err := json.Unmarshal(blob, &officer); err != nil {
			return nil, &OfficerRegistryError{Op: "unmarshal", Err: err}
		}
		// Defensive: backfill the username from the key in case the
		// payload was corrupted (best-effort consistency).
		if officer.Username == "" {
			officer.Username = username
		}
		cp := officer
		r.cache[officer.Username] = &cp
		out = append(out, &officer)
	}

	sortOfficersByEnrolledAt(out)
	return out, nil
}

// MarkClaimed updates the ClaimedAt timestamp on the named officer's
// record. The record must already exist.
//
// Returns ErrUserNotFound if the officer is not enrolled, or ErrSealed
// if the underlying barrier is sealed.
func (r *OfficerRegistry) MarkClaimed(ctx context.Context, username string, when time.Time) error {
	if username == "" {
		return ErrMissingUsername
	}
	if when.IsZero() {
		when = time.Now().UTC()
	} else {
		when = when.UTC()
	}

	// Load through the public API so sealed-state semantics propagate.
	current, err := r.Load(ctx, username)
	if err != nil {
		return err
	}

	current.ClaimedAt = when
	return r.Save(ctx, current)
}

// Reload empties the in-memory cache and re-populates it from the backend.
// This is intended to be called after barrier unseal so the in-memory
// view is consistent with what the data plane sees.
//
// Returns ErrSealed when invoked while the underlying barrier is sealed.
func (r *OfficerRegistry) Reload(ctx context.Context) error {
	keys, err := r.backend.List(ctx, r.prefix)
	if err != nil {
		return err
	}

	rebuilt := make(map[string]*PersistentOfficer, len(keys))
	for _, k := range keys {
		blob, getErr := r.backend.Get(ctx, k)
		if getErr != nil {
			return getErr
		}
		var officer PersistentOfficer
		if err := json.Unmarshal(blob, &officer); err != nil {
			return &OfficerRegistryError{Op: "unmarshal", Err: err}
		}
		if officer.Username == "" {
			officer.Username = strings.TrimPrefix(k, r.prefix)
		}
		cp := officer
		rebuilt[officer.Username] = &cp
	}

	r.mu.Lock()
	r.cache = rebuilt
	r.mu.Unlock()
	return nil
}

// ResetClaimedAt clears the ClaimedAt timestamp on every enrolled officer,
// marking a new share distribution round. Each officer record is updated in
// the cache and persisted best-effort (sealed-barrier tolerance: a failed
// write is logged but does not stop the reset). The cache is always updated
// even when persistence fails so the in-memory view is immediately consistent.
func (r *OfficerRegistry) ResetClaimedAt(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for username, officer := range r.cache {
		if officer.ClaimedAt.IsZero() {
			continue
		}
		officer.ClaimedAt = time.Time{}
		cp := *officer
		r.cache[username] = &cp

		blob, err := json.Marshal(&cp)
		if err != nil {
			continue // best-effort
		}
		_ = r.backend.Put(ctx, r.officerKey(username), blob) // best-effort
	}
	return nil
}

// Cached returns a snapshot of the in-memory cache without consulting
// the backend. Used by callers that need a synchronous view (for example
// during barrier-sealed Status reporting). The returned slice is a
// defensive copy.
func (r *OfficerRegistry) Cached() []*PersistentOfficer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*PersistentOfficer, 0, len(r.cache))
	for _, v := range r.cache {
		cp := *v
		out = append(out, &cp)
	}
	sortOfficersByEnrolledAt(out)
	return out
}

// officerKey returns the storage key for the given username using this
// registry's configured prefix.
func (r *OfficerRegistry) officerKey(username string) string {
	return r.prefix + username
}

// Prefix returns the storage prefix configured for this registry.
func (r *OfficerRegistry) Prefix() string {
	return r.prefix
}

// sortOfficersByEnrolledAt sorts officers in ascending enrolled-at order
// with username as a stable tiebreaker. Avoids importing sort.Slice for
// readability while keeping the comparison strict.
func sortOfficersByEnrolledAt(officers []*PersistentOfficer) {
	// Insertion sort is fine for the expected M-of-N officer counts (<32).
	for i := 1; i < len(officers); i++ {
		j := i
		for j > 0 && officerLess(officers[j], officers[j-1]) {
			officers[j], officers[j-1] = officers[j-1], officers[j]
			j--
		}
	}
}

// officerLess returns true if a should sort before b.
func officerLess(a, b *PersistentOfficer) bool {
	if a.EnrolledAt.Equal(b.EnrolledAt) {
		return a.Username < b.Username
	}
	return a.EnrolledAt.Before(b.EnrolledAt)
}

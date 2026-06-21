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

package seal

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// migrationJournalSuffix is appended to the configured RootKeyPath to form the
// storage key under which the encryption-migration journal is persisted. Like
// the barrier metadata it is a SIBLING of the root key (a "." suffix), never a
// child, so it never nests under the secret. The journal contains no secret
// material — only migration progress state — and is written in plaintext so a
// warm-booted barrier can detect and resume (or roll back) an interrupted
// migration before serving traffic.
const migrationJournalSuffix = ".migration"

// MigrationMechanism selects how the at-rest store is physically re-encrypted
// when enabling encryption on a previously-unencrypted barrier.
type MigrationMechanism string

const (
	// MechanismSwap re-encrypts by streaming the entire dataset through the
	// engine into a freshly-keyed staging store (snapshot Export/Import),
	// verifying it, then atomically swapping the staging store in for the
	// plaintext store. The directory swap is the single commit point, which
	// makes the operation crash-safe: a crash before the swap leaves the
	// original plaintext store intact; a crash after leaves the keyed store
	// intact. Requires roughly 2x peak disk during the copy.
	MechanismSwap MigrationMechanism = "swap"

	// MechanismInPlace re-encrypts each physical store file in place: read it
	// through the source (plaintext / none) encrypting FS and rewrite it
	// through the keyed encrypting FS to a temp file, then rename over the
	// original. A per-file journal records progress so an interrupted run
	// resumes from the next unconverted file. Lower peak disk than swap, at
	// the cost of a more delicate crash-safety contract.
	MechanismInPlace MigrationMechanism = "in-place"
)

// valid reports whether m is a known mechanism.
func (m MigrationMechanism) valid() bool {
	return m == MechanismSwap || m == MechanismInPlace
}

// MigrationAvailability selects how the node serves writes while a migration
// runs.
type MigrationAvailability string

const (
	// AvailabilityQuiesced blocks writes for the duration of the migration on
	// the node being converted (a maintenance window). It is the simplest mode
	// to reason about and is provably consistent — no writes race the
	// re-encryption. In a Raft cluster, replicas are migrated on a rolling
	// basis so the database as a whole stays available.
	AvailabilityQuiesced MigrationAvailability = "quiesced"

	// AvailabilityOnline keeps serving writes while re-encryption proceeds in
	// the background, then replays the delta accumulated during the copy before
	// committing. More complex and higher risk than quiesced; used when a
	// maintenance window is unacceptable.
	AvailabilityOnline MigrationAvailability = "online"
)

// valid reports whether a is a known availability mode.
func (a MigrationAvailability) valid() bool {
	return a == AvailabilityQuiesced || a == AvailabilityOnline
}

// MigrationPhase tracks the crash-safe progress of an encryption migration.
// The phase is the resume/rollback authority: on warm boot the barrier reads
// the journal and, based on the phase, either resumes the migration or rolls
// back to the original plaintext store.
type MigrationPhase string

const (
	// PhasePending: journal written, no irreversible work done yet.
	PhasePending MigrationPhase = "pending"
	// PhasePreparing: keyed barrier minted and staging store created.
	PhasePreparing MigrationPhase = "preparing"
	// PhaseCopying: data is being re-encrypted into the keyed store/files.
	PhaseCopying MigrationPhase = "copying"
	// PhaseVerifying: re-encrypted data is being verified against the source.
	PhaseVerifying MigrationPhase = "verifying"
	// PhaseCommitting: the atomic swap / strategy flip is in progress. This is
	// the point of no return; recovery rolls forward, never back.
	PhaseCommitting MigrationPhase = "committing"
	// PhaseComplete: migration finished; the journal is removed shortly after.
	PhaseComplete MigrationPhase = "complete"
	// PhaseFailed: migration aborted before the commit point; recovery rolls
	// back to the original plaintext store.
	PhaseFailed MigrationPhase = "failed"
)

// terminal reports whether the phase is an end state (no further work).
func (p MigrationPhase) terminal() bool {
	return p == PhaseComplete || p == PhaseFailed
}

// committed reports whether the migration has passed the point of no return.
// Recovery from a committed (or complete) journal must roll FORWARD to the
// keyed store; it must never restore the plaintext source.
func (p MigrationPhase) committed() bool {
	return p == PhaseCommitting || p == PhaseComplete
}

// MigrationOptions describes a request to enable encryption on a
// none-strategy barrier by migrating its at-rest data to a keyed strategy.
type MigrationOptions struct {
	// TargetStrategy is the keyed strategy to migrate to (StrategyPassphrase
	// or StrategyShamir). It must not be StrategyNone.
	TargetStrategy StrategyID

	// Mechanism selects the physical re-encryption approach.
	Mechanism MigrationMechanism

	// Availability selects the write-availability behaviour during migration.
	Availability MigrationAvailability

	// Credentials carry the passphrase for a passphrase-target migration.
	// Ignored for Shamir targets, which derive their unseal material from the
	// share-quorum ceremony.
	Credentials Credentials

	// ShamirThreshold / ShamirTotal configure the M-of-N scheme for a
	// Shamir-target migration. Ignored for passphrase targets.
	ShamirThreshold int
	ShamirTotal     int
}

// Validate checks the options for internal consistency, returning a typed
// error on the first problem found. It does NOT check the barrier's current
// state (that is the caller's responsibility at migration start).
func (o MigrationOptions) Validate() error {
	switch o.TargetStrategy {
	case StrategyNone, "":
		return ErrMigrationInvalidTarget
	}
	if !o.Mechanism.valid() {
		return ErrMigrationInvalidMechanism
	}
	if !o.Availability.valid() {
		return ErrMigrationInvalidAvailability
	}
	if o.TargetStrategy == StrategyShamir {
		if o.ShamirThreshold < 2 || o.ShamirTotal < o.ShamirThreshold {
			return ErrShamirThresholdInvalid
		}
	}
	return nil
}

// MigrationJournal is the crash-safe, plaintext record of an in-progress (or
// recently completed) encryption migration. It is persisted to the barrier's
// storage backend at migrationJournalPath so that a process restart can detect
// and recover an interrupted migration. It contains no secret material.
type MigrationJournal struct {
	// SourceStrategy is the strategy the barrier had before migration (none).
	SourceStrategy StrategyID `json:"source_strategy"`
	// TargetStrategy is the keyed strategy being migrated to.
	TargetStrategy StrategyID `json:"target_strategy"`
	// Mechanism / Availability echo the options for resume decisions.
	Mechanism    MigrationMechanism    `json:"mechanism"`
	Availability MigrationAvailability `json:"availability"`
	// Phase is the recovery authority (see MigrationPhase).
	Phase MigrationPhase `json:"phase"`
	// StagingRootKeyPath is the root-key path of the freshly-keyed sibling
	// barrier used by the swap mechanism. Empty for in-place migrations.
	StagingRootKeyPath string `json:"staging_root_key_path,omitempty"`
	// Stores maps a logical store identifier (e.g. "mvcc", "vector", "ts") to
	// whether its re-encryption has completed. Used to resume multi-store
	// migrations from where they left off.
	Stores map[string]bool `json:"stores,omitempty"`
	// StartedAt / UpdatedAt are UTC timestamps for observability and audit.
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Error records the failure reason when Phase == PhaseFailed.
	Error string `json:"error,omitempty"`
}

// RootKeyPath returns the storage key under which this barrier's sealed root
// key is persisted. Exposed so an encryption migration can record the staging
// barrier's path in its journal for crash recovery.
func (b *StorageBarrier) RootKeyPath() string { return b.config.RootKeyPath }

// LoadMigrationJournalFrom reads the migration journal directly from a storage
// backend (without a constructed barrier), for crash recovery during boot
// before the barrier is unsealed. rootKeyPath is the canonical root-key path;
// the journal lives at rootKeyPath + the journal suffix. Returns (nil, nil)
// when no journal is present.
func LoadMigrationJournalFrom(ctx context.Context, backend StorageBackend, rootKeyPath string) (*MigrationJournal, error) {
	blob, err := backend.Get(ctx, rootKeyPath+migrationJournalSuffix)
	if err != nil {
		if isSecretNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(blob) == 0 {
		return nil, nil
	}
	var j MigrationJournal
	if err := json.Unmarshal(blob, &j); err != nil {
		return nil, err
	}
	return &j, nil
}

// ClearMigrationJournalFrom removes the migration journal directly from a
// backend (crash-recovery cleanup). A missing journal is not an error.
func ClearMigrationJournalFrom(ctx context.Context, backend StorageBackend, rootKeyPath string) error {
	err := backend.Delete(ctx, rootKeyPath+migrationJournalSuffix)
	if err != nil && isSecretNotFound(err) {
		return nil
	}
	return err
}

// PersistedStrategy returns the sealing strategy recorded in the persisted
// sealed root-key blob, or "" when the barrier is not yet initialized or the
// blob cannot be read. It lets boot logic discover a barrier that was migrated
// to a keyed strategy after the fact (enable-encryption) so it can auto-unseal
// with the migrated strategy even when the static config still names the
// pre-migration one — the barrier is self-describing from its persisted state.
func (b *StorageBarrier) PersistedStrategy(ctx context.Context) StrategyID {
	return PersistedStrategyFrom(ctx, b.base, b.config.RootKeyPath)
}

// PersistedStrategyFrom reads the sealing strategy recorded in the sealed
// root-key blob at rootKeyPath in the given backend, returning "" when no blob
// exists or it cannot be decoded. It is the barrier-free form of
// PersistedStrategy, for boot logic that must decide which strategies to
// register BEFORE constructing the StorageBarrier — e.g. a barrier migrated
// none->keyed via enable-encryption must register the migrated keyed strategy
// even when the static config still names "none", so the re-init can unseal it.
func PersistedStrategyFrom(ctx context.Context, backend StorageBackend, rootKeyPath string) StrategyID {
	blob, err := backend.Get(ctx, rootKeyPath)
	if err != nil || len(blob) == 0 {
		return ""
	}
	var sealed struct {
		Strategy StrategyID `json:"strategy"`
	}
	if json.Unmarshal(blob, &sealed) != nil {
		return ""
	}
	return sealed.Strategy
}

// Committed reports whether the migration has passed the point of no return
// (PhaseCommitting or PhaseComplete). Recovery from a committed journal must
// roll FORWARD to the keyed store and never restore the plaintext source.
func (j *MigrationJournal) Committed() bool { return j.Phase.committed() }

// Terminal reports whether the migration has reached an end state.
func (j *MigrationJournal) Terminal() bool { return j.Phase.terminal() }

// migrationJournalPath returns the storage key for the barrier's migration
// journal (a sibling of the root key).
func (b *StorageBarrier) migrationJournalPath() string {
	return b.config.RootKeyPath + migrationJournalSuffix
}

// LoadMigrationJournal reads the persisted migration journal, returning
// (nil, nil) when no migration is in progress (no journal present). A present
// but malformed journal is reported as ErrCorruptRootKey's sibling: callers
// treat a decode failure as a corrupt journal and must not proceed blindly.
func (b *StorageBarrier) LoadMigrationJournal(ctx context.Context) (*MigrationJournal, error) {
	blob, err := b.base.Get(ctx, b.migrationJournalPath())
	if err != nil {
		// A missing journal is not an error: most barriers have none.
		if isSecretNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(blob) == 0 {
		return nil, nil
	}
	var j MigrationJournal
	if err := json.Unmarshal(blob, &j); err != nil {
		return nil, err
	}
	return &j, nil
}

// writeMigrationJournal persists the journal blob, stamping UpdatedAt. It is
// the single writer of the journal; callers mutate a *MigrationJournal and
// call this to checkpoint progress.
func (b *StorageBarrier) writeMigrationJournal(ctx context.Context, j *MigrationJournal) error {
	j.UpdatedAt = time.Now().UTC()
	blob, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return b.base.Put(ctx, b.migrationJournalPath(), blob)
}

// clearMigrationJournal removes the migration journal after a migration
// reaches a terminal, fully-reconciled state. Best-effort: a missing journal
// is not an error.
func (b *StorageBarrier) clearMigrationJournal(ctx context.Context) error {
	err := b.base.Delete(ctx, b.migrationJournalPath())
	if err != nil && isSecretNotFound(err) {
		return nil
	}
	return err
}

// beginMigrationJournal validates options, ensures no migration is already in
// flight, and writes the initial PhasePending journal. It returns the journal
// the caller threads through the migration, checkpointing it via
// writeMigrationJournal as phases advance.
func (b *StorageBarrier) beginMigrationJournal(ctx context.Context, opts MigrationOptions) (*MigrationJournal, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	// Enabling encryption is only defined from credential-free source strategies:
	// StrategyNone (plaintext, unencrypted) and StrategySystemBarrier (the tenant
	// root key is wrapped by the system barrier and can be unwrapped credential-free
	// whenever the system barrier is unsealed). User-credential-bound strategies
	// (passphrase, Shamir) are not a valid migration source.
	active, _ := b.activeStrategy.Load().(StrategyID)
	if active != StrategyNone && active != StrategySystemBarrier {
		return nil, ErrMigrationSourceNotNone
	}
	existing, err := b.LoadMigrationJournal(ctx)
	if err != nil {
		return nil, err
	}
	if existing != nil && !existing.Phase.terminal() {
		return nil, ErrMigrationInProgress
	}
	now := time.Now().UTC()
	j := &MigrationJournal{
		SourceStrategy: active,
		TargetStrategy: opts.TargetStrategy,
		Mechanism:      opts.Mechanism,
		Availability:   opts.Availability,
		Phase:          PhasePending,
		Stores:         map[string]bool{},
		StartedAt:      now,
		UpdatedAt:      now,
	}
	if err := b.writeMigrationJournal(ctx, j); err != nil {
		return nil, err
	}
	return j, nil
}

// BeginMigration validates opts, ensures the barrier is a credential-free source
// (StrategyNone or StrategySystemBarrier) with no migration already in flight,
// and writes the initial journal. The returned journal is threaded through the
// migration and checkpointed via CheckpointMigration as phases advance. It is
// the exported entrypoint the data-plane orchestrator uses.
func (b *StorageBarrier) BeginMigration(ctx context.Context, opts MigrationOptions) (*MigrationJournal, error) {
	return b.beginMigrationJournal(ctx, opts)
}

// CheckpointMigration persists the current journal state (stamping UpdatedAt),
// recording crash-safe progress between phases.
func (b *StorageBarrier) CheckpointMigration(ctx context.Context, j *MigrationJournal) error {
	return b.writeMigrationJournal(ctx, j)
}

// FinishMigration removes the journal after a migration reaches a terminal,
// fully-reconciled state.
func (b *StorageBarrier) FinishMigration(ctx context.Context) error {
	return b.clearMigrationJournal(ctx)
}

// DeleteRootKey removes this barrier's persisted sealed root-key blob from the
// backend. It is used to clean up a STAGING sibling barrier (minted via
// MintKeyedSibling) when a migration rolls back: without this, the orphaned
// staging blob makes the next MintKeyedSibling's Initialize fail with
// ErrAlreadyInitialized, permanently wedging every retry. Unlike Delete it is not
// seal-gated — cleanup of a staging blob must succeed regardless of seal state. A
// missing blob is not an error.
func (b *StorageBarrier) DeleteRootKey(ctx context.Context) error {
	if err := b.base.Delete(ctx, b.config.RootKeyPath); err != nil && !isSecretNotFound(err) {
		return err
	}
	return nil
}

// isSecretNotFound reports whether err signals a missing backend key,
// centralising the sentinel check the journal loader/clearer share with the
// metadata loader.
func isSecretNotFound(err error) bool {
	return errors.Is(err, ErrSecretNotFound)
}

// The concrete re-encryption Migrator (swap / in-place) operates on physical
// Pebble store directories and therefore lives at the data-plane layer
// (pkg/database), which owns Pebble + pkg/atrest knowledge. pkg/seal owns only
// the barrier-side primitives above (options, journal, key material, and the
// strategy transition below).

// MintKeyedSibling creates and initializes a fresh keyed barrier over THIS
// barrier's exact backend and config — same StorageBackend, same DeploymentID
// (which participates in DEK derivation) — at the root-key path
// b.RootKeyPath()+stagingSuffix. It is the uniform way an encryption migration
// mints its target for either the system barrier or a per-tenant barrier: by
// mirroring the source's backend + config, the keyed data it produces is
// reproducible by the normal boot path, and the sibling's sealed root key can
// be adopted onto the canonical path at commit (AdoptMigratedRootKey).
//
// strategy must be a keyed strategy and SHOULD be the same implementation the
// boot path registers for that strategy ID (e.g. NewDeferredSoftwareStrategy
// for passphrase) so the sealed blob is unsealable after a restart. creds carry
// the unseal credential.
func (b *StorageBarrier) MintKeyedSibling(ctx context.Context, stagingSuffix string, strategy SealingStrategy, creds Credentials) (*StorageBarrier, error) {
	cfg := BarrierConfig{
		RootKeyPath:  b.config.RootKeyPath + stagingSuffix,
		DeploymentID: b.config.DeploymentID,
	}
	sib, err := NewStorageBarrier(b.logger, b.base, cfg, strategy)
	if err != nil {
		return nil, err
	}
	// The staging root-key path is owned EXCLUSIVELY by the migration subsystem
	// (a fixed suffix off this barrier's path), so any pre-existing blob there is
	// an abandoned staging key from a prior interrupted migration — never a live
	// barrier. Clear it before Initialize so the first-writer CAS in Initialize
	// does not fail with ErrAlreadyInitialized. This makes mint idempotent and
	// self-healing: a migration can always be retried (in-process or after a
	// crash) without every caller having to remember to delete the staging blob on
	// its rollback path. Concurrency-safe because per-barrier migrations are
	// serialized by the caller's migrating guard.
	if derr := sib.DeleteRootKey(ctx); derr != nil {
		return nil, derr
	}
	if err := sib.Initialize(ctx, creds); err != nil {
		return nil, err
	}
	return sib, nil
}

// ReopenKeyed constructs a fresh barrier over THIS barrier's backend and config
// (canonical RootKeyPath, same DeploymentID) using the given keyed strategy and
// unseals it. It is the post-commit step of an encryption migration: after
// AdoptMigratedRootKey has written the keyed root key onto the canonical path,
// the live (none) barrier is replaced with the value ReopenKeyed returns. The
// caller has already quiesced the stores the barrier protects.
func (b *StorageBarrier) ReopenKeyed(ctx context.Context, strategy SealingStrategy, creds Credentials) (*StorageBarrier, error) {
	cfg := BarrierConfig{
		RootKeyPath:  b.config.RootKeyPath,
		DeploymentID: b.config.DeploymentID,
	}
	nb, err := NewStorageBarrier(b.logger, b.base, cfg, strategy)
	if err != nil {
		return nil, err
	}
	if err := nb.Unseal(ctx, creds); err != nil && !errors.Is(err, ErrAlreadyUnsealed) {
		return nil, err
	}
	return nb, nil
}

// AdoptMigratedRootKey commits an encryption migration on the barrier side: it
// overwrites this (none) barrier's persisted root-key blob with the keyed root
// key from target and transitions the active strategy, so that on the next
// boot the node unseals via the keyed strategy. It is the barrier half of the
// migration commit; the caller has already re-encrypted and swapped the data
// stores and is holding the migration journal at PhaseCommitting.
//
// This deliberately overwrites the canonical root-key path (bypassing
// Initialize's first-writer CAS) because adopting the migrated key is the whole
// point of the operation. It must only be called as the commit step of a
// journalled migration, after the keyed data stores are durably in place.
//
// target must be an initialized, unsealed keyed barrier (StrategyPassphrase or
// StrategyShamir); both barriers share this package so the keyed sealed blob is
// read directly from target.
func (b *StorageBarrier) AdoptMigratedRootKey(ctx context.Context, target *StorageBarrier) error {
	if target == nil {
		return ErrNilSealedData
	}
	targetStrategy, _ := target.activeStrategy.Load().(StrategyID)
	switch targetStrategy {
	case StrategyPassphrase, StrategyShamir:
		// ok — keyed target.
	default:
		return ErrMigrationInvalidTarget
	}

	// Capture the source strategy before overwriting it so the audit log is accurate
	// regardless of whether the source was StrategyNone or StrategySystemBarrier.
	sourceStrategy, _ := b.activeStrategy.Load().(StrategyID)

	sealed := target.barrier.SealedRootKeyData()
	if sealed == nil {
		return ErrNilSealedData
	}
	blob, err := json.Marshal(sealed)
	if err != nil {
		return err
	}
	// Overwrite the canonical root-key blob with the keyed sealed root key.
	if err := b.base.Put(ctx, b.config.RootKeyPath, blob); err != nil {
		return err
	}
	// Transition the in-memory active strategy so Status() reflects the keyed
	// strategy immediately; the durable transition is the blob above.
	b.activeStrategy.Store(targetStrategy)

	// Refresh the initialization metadata timestamp (the keyed barrier is, for
	// audit purposes, newly established at migration time).
	if err := b.persistMetadata(ctx, barrierMetadata{InitializedAt: time.Now().UTC()}); err != nil {
		return err
	}

	emitAudit(b.config.AuditLogger, "enable_encryption", "barrier", b.config.RootKeyPath,
		"allow", map[string]string{"from": string(sourceStrategy), "to": string(targetStrategy)})
	return nil
}

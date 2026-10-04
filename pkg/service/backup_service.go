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

package service

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/keywrap"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// ---------------------------------------------------------------------------
// Backup types
// ---------------------------------------------------------------------------

// CompressionType identifies the compression algorithm for a backup.
type CompressionType string

const (
	CompressionNone   CompressionType = "none"
	CompressionZstd   CompressionType = "zstd"
	CompressionSnappy CompressionType = "snappy"
)

// EncryptionType identifies the encryption algorithm for a backup.
type EncryptionType string

const (
	// EncryptionNone writes the backup in plaintext. It is never applied
	// implicitly: a caller gets it only by naming it explicitly in
	// BackupOptions.Encryption, which CreateBackup logs at WARN and, when
	// an audit logger is configured, records in the compliance audit
	// trail -- a backup covering the system shard carries the cluster CA
	// and every tenant's login CA key material, so writing one in the
	// clear is a decision this package makes visible, never one it makes
	// quietly on a caller's behalf.
	EncryptionNone EncryptionType = "none"

	// EncryptionAES256 encrypts the backup with a fresh per-backup
	// AES-256-GCM data-encrypting key (DEK), itself wrapped under a
	// key-encrypting key (KEK) resolved through pkg/keywrap (see
	// BackupOptions.KeyProviderID / KeyRef). It is CreateBackup's default
	// when BackupOptions.Encryption is left empty -- see
	// BackupEncryptionRequiredError.
	EncryptionAES256 EncryptionType = "aes256"
)

// BackupStatus describes the current state of a backup.
type BackupStatus string

const (
	BackupStatusPending  BackupStatus = "pending"
	BackupStatusRunning  BackupStatus = "running"
	BackupStatusComplete BackupStatus = "complete"
	BackupStatusFailed   BackupStatus = "failed"
)

// BackupOptions contains parameters for creating a backup.
type BackupOptions struct {
	Destination string
	Compression CompressionType

	// Encryption selects the encryption algorithm. Left empty, CreateBackup
	// defaults it to EncryptionAES256 and then requires KeyRef: the
	// default is encrypted, not plaintext, and a caller that wants
	// plaintext must say EncryptionNone outright rather than get it by
	// omission. See BackupEncryptionRequiredError.
	Encryption EncryptionType

	// KeyProviderID names the registered keywrap.Provider to wrap this
	// backup's DEK with. Defaults to keywrap.SoftwareProviderID ("software")
	// when KeyRef is set and this is left empty.
	KeyProviderID string

	// KeyRef is the key reference the named provider resolves -- a
	// key-file path for the "software" provider, an HSM slot/handle or a
	// KMS key ARN for a hardware or cloud-KMS provider go-xkms registers.
	// Required whenever Encryption resolves to EncryptionAES256 (whether
	// defaulted or named explicitly): there is no inline raw-key escape
	// hatch -- every KEK is referenced through a provider, never passed as
	// bytes through this struct.
	KeyRef string

	Metadata map[string]string
	ShardIDs []uint64
}

// BackupInfo contains metadata about a backup.
type BackupInfo struct {
	ID          string
	Timestamp   time.Time
	Size        int64
	ShardCount  int
	Status      BackupStatus
	Checksum    string
	Destination string
	Compression CompressionType
	Encryption  EncryptionType

	// KeyProviderID is the keywrap.Provider that wrapped this backup's DEK.
	// Empty for an EncryptionNone backup. Never accompanied on this type by
	// the wrapped DEK, the key reference, or the envelope format version --
	// those live only in the backup's own persisted envelope (see
	// BackupEnvelope), read back internally by RestoreBackup/VerifyBackup.
	KeyProviderID string

	Metadata     map[string]string
	Duration     time.Duration
	ErrorMessage string
}

// BackupEnvelope carries a backup's envelope-encryption metadata: which
// KEK provider wrapped this backup's DEK, the key reference it was wrapped
// under, the wrapped DEK itself, and the envelope format version. It is
// never exposed on BackupInfo -- only BackupInfo.KeyProviderID is -- and is
// persisted alongside a backup's metadata and shard data wherever those
// already live: the in-memory registry, or the configured BackupSink. Read
// back only by RestoreBackup and VerifyBackup.
type BackupEnvelope struct {
	// FormatVersion is recorded, not inferred, so a future envelope format
	// change can still read an old backup correctly. Version 1 is AES-256-GCM
	// at both layers with AAD binding each shard to this backup's metadata
	// (see buildShardAAD).
	FormatVersion uint32

	// ProviderID is the keywrap.Provider that wrapped WrappedDEK. Resolved
	// through the registry at restore time -- a backup never chooses a
	// different provider than the one that wrapped its DEK.
	ProviderID string

	// KeyRef is the key reference ProviderID was asked to Wrap/Unwrap
	// under -- a key-file path for the software provider. Restore may
	// override this with RestoreOptions.DecryptionKeyRef (e.g. the key
	// file moved, or restore runs on a different node), but defaults to
	// this stored value.
	KeyRef string

	// WrappedDEK is this backup's single per-backup data-encrypting key,
	// wrapped under the KEK ProviderID/KeyRef name. Never the DEK itself.
	WrappedDEK []byte
}

// RestoreOptions contains parameters for restoring a backup.
type RestoreOptions struct {
	BackupID      string
	TargetShards  []uint64
	ValidateFirst bool

	// DecryptionKeyRef overrides the key reference stored in the backup's
	// own envelope (BackupEnvelope.KeyRef) -- useful when restoring on a
	// different node, or after the key file has moved. Left empty, the
	// stored reference is used. The provider actually used to Unwrap is
	// always whichever one the backup was created under
	// (BackupEnvelope.ProviderID); this field never selects a different
	// provider than that.
	DecryptionKeyRef string
}

// RestoreResult describes the outcome of a restore operation.
type RestoreResult struct {
	BackupID       string
	ShardsRestored int
	Duration       time.Duration
	Errors         []ShardError
}

// ShardError describes a per-shard error during backup or restore.
type ShardError struct {
	ShardID uint64
	Message string
}

// VerifyResult describes the outcome of a backup verification.
type VerifyResult struct {
	Valid            bool
	ChecksumMatch    bool
	ExpectedChecksum string
	ActualChecksum   string
	Errors           []string
}

// ---------------------------------------------------------------------------
// Snapshot provider interface
// ---------------------------------------------------------------------------

// ShardSnapshotProvider abstracts shard snapshot access for the backup
// service. This allows the backup service to iterate shard data without
// being tightly coupled to the Raft or storage layer.
type ShardSnapshotProvider interface {
	// AvailableShards returns the shard IDs available for backup.
	AvailableShards(ctx context.Context) ([]uint64, error)

	// SnapshotShard captures a point-in-time snapshot of the specified
	// shard and returns the raw data bytes.
	SnapshotShard(ctx context.Context, shardID uint64) ([]byte, error)

	// RestoreShard restores the specified shard from raw data bytes.
	RestoreShard(ctx context.Context, shardID uint64, data []byte) error
}

// ---------------------------------------------------------------------------
// Backup sink (durable repository)
// ---------------------------------------------------------------------------

// BackupSink is a durable repository for backups. When a BackupService is
// configured with a sink, backup shard data is persisted to (and restored from)
// the sink instead of being held in process memory — the sink is the source of
// truth and survives restarts. Implementations include an object-store-backed
// sink (see pkg/service/backupsink). When no sink is configured the service
// keeps backups in memory only (non-durable; suitable for tests).
type BackupSink interface {
	// Write persists a backup's metadata, shard data, and (for an
	// encrypted backup) its envelope. envelope is nil for an EncryptionNone
	// backup.
	Write(ctx context.Context, info BackupInfo, shards map[uint64][]byte, envelope *BackupEnvelope) error

	// ReadInfo returns a backup's metadata without loading its shard data.
	ReadInfo(ctx context.Context, id string) (*BackupInfo, error)

	// ReadShards loads a backup's shard data.
	ReadShards(ctx context.Context, id string) (map[uint64][]byte, error)

	// ReadEnvelope loads a backup's envelope metadata. Returns a nil
	// envelope (and a nil error) for an EncryptionNone backup, which
	// carries none.
	ReadEnvelope(ctx context.Context, id string) (*BackupEnvelope, error)

	// List returns metadata for all persisted backups.
	List(ctx context.Context) ([]BackupInfo, error)

	// Delete removes a persisted backup.
	Delete(ctx context.Context, id string) error
}

// ---------------------------------------------------------------------------
// BackupServicer interface
// ---------------------------------------------------------------------------

// BackupServicer defines backup and restore operations.
type BackupServicer interface {
	// CreateBackup initiates a new backup with the specified options.
	CreateBackup(ctx context.Context, opts BackupOptions) (*BackupInfo, error)

	// ListBackups returns metadata for all available backups.
	ListBackups(ctx context.Context) ([]BackupInfo, error)

	// GetBackup returns metadata for a specific backup by ID.
	GetBackup(ctx context.Context, id string) (*BackupInfo, error)

	// DeleteBackup removes a backup by ID.
	DeleteBackup(ctx context.Context, id string) error

	// RestoreBackup restores data from a backup.
	RestoreBackup(ctx context.Context, opts RestoreOptions) (*RestoreResult, error)

	// VerifyBackup verifies the integrity of a backup by computing
	// and comparing checksums.
	VerifyBackup(ctx context.Context, id string) (*VerifyResult, error)
}

// ---------------------------------------------------------------------------
// BackupService implementation
// ---------------------------------------------------------------------------

// backupEntry stores backup metadata, data, and envelope together.
type backupEntry struct {
	info     BackupInfo
	shards   map[uint64][]byte
	envelope *BackupEnvelope
}

// BackupService implements BackupServicer using a ShardSnapshotProvider
// for shard data access and an in-memory backup registry.
type BackupService struct {
	provider ShardSnapshotProvider
	sink     BackupSink
	mu       sync.RWMutex
	backups  map[string]*backupEntry
	idGen    func() string

	// keyWrap resolves the keywrap.Provider a backup's KeyProviderID names.
	// Defaults to keywrap.NewDefaultRegistry() (pre-populated with
	// "software") when no WithKeyWrapRegistry/WithKeyWrapProvider option is
	// applied, so the common case -- a key-file path and nothing else --
	// works with no composition required.
	keyWrap keywrap.Registry

	// keyWrapExtra is a single additional provider WithKeyWrapProvider
	// supplies, registered into keyWrap at construction time under its own
	// ProviderID.
	keyWrapExtra keywrap.Provider

	// audit is an optional compliance-grade audit logger. When set, a
	// backup created with encryption explicitly disabled (EncryptionNone)
	// emits an audit event recording that choice, in addition to the WARN
	// log every such backup gets regardless. A nil logger disables
	// emission. It is read via auditLogger()/SetAuditLogger under mu so a
	// composer can attach it after construction, the same accommodation
	// pkg/credentials.CredentialService makes for the same reason (the
	// seal subsystem builds services before the compliance logger
	// exists).
	audit audit.AuditLogger
}

// Compile-time interface assertion.
var _ BackupServicer = (*BackupService)(nil)

// BackupServiceOption configures optional BackupService behavior.
type BackupServiceOption func(*BackupService)

// WithIDGenerator sets a custom ID generator for deterministic testing.
func WithIDGenerator(gen func() string) BackupServiceOption {
	return func(s *BackupService) {
		s.idGen = gen
	}
}

// WithBackupSink configures a durable backup repository. When set, shard data is
// persisted to the sink rather than held in process memory.
func WithBackupSink(sink BackupSink) BackupServiceOption {
	return func(s *BackupService) {
		s.sink = sink
	}
}

// WithAuditLogger attaches a compliance-grade audit logger so a backup
// created with encryption explicitly disabled leaves an audit trail record,
// in addition to its WARN log line. A nil logger disables emission, so the
// option is safe to apply unconditionally.
func WithAuditLogger(logger audit.AuditLogger) BackupServiceOption {
	return func(s *BackupService) {
		s.audit = logger
	}
}

// WithKeyWrapRegistry configures the keywrap.Registry used to resolve a
// backup's KeyProviderID, replacing the default (software-only) registry.
// go-xkms and other embedders use this to install a registry carrying
// hardware or cloud-KMS providers, the same With* composition shape
// seal.WithStrategyRegistry uses for sealing strategies.
func WithKeyWrapRegistry(r keywrap.Registry) BackupServiceOption {
	return func(s *BackupService) {
		s.keyWrap = r
	}
}

// WithKeyWrapProvider registers a single additional keywrap.Provider under
// its own ProviderID into the (default, software-only, unless
// WithKeyWrapRegistry was also applied) registry. The narrower sibling of
// WithKeyWrapRegistry for an embedder with exactly one extra provider and
// no registry of its own to supply.
func WithKeyWrapProvider(p keywrap.Provider) BackupServiceOption {
	return func(s *BackupService) {
		s.keyWrapExtra = p
	}
}

// SetAuditLogger attaches (or replaces) the compliance audit logger after
// construction. It exists for wiring paths where the service is built
// before the daemon's compliance logger is available. A nil logger
// disables emission. Safe to call once during boot before the service
// handles requests.
func (s *BackupService) SetAuditLogger(logger audit.AuditLogger) {
	s.mu.Lock()
	s.audit = logger
	s.mu.Unlock()
}

// auditLogger returns the configured audit logger under mu.
func (s *BackupService) auditLogger() audit.AuditLogger {
	s.mu.RLock()
	l := s.audit
	s.mu.RUnlock()
	return l
}

// warnPlaintextBackup logs, at WARN, that a backup is being written with
// encryption explicitly disabled, and -- when an audit logger is
// configured -- records the same fact in the compliance audit trail. It is
// called only for an EXPLICIT EncryptionNone; the default (empty
// BackupOptions.Encryption) never reaches here because it is refused before
// any backup starts (BackupEncryptionRequiredError) unless a key was also
// supplied, in which case it is encrypted, not plaintext.
func (s *BackupService) warnPlaintextBackup(ctx context.Context, destination string) {
	slog.WarnContext(ctx, "service: backup created with encryption explicitly disabled; the backup is plaintext, and one covering the system shard carries the cluster CA and every tenant's login CA key material in the clear",
		"destination", destination)

	logger := s.auditLogger()
	if logger == nil {
		return
	}
	var actor, tenantID string
	if id := auth.IdentityFromContext(ctx); id != nil {
		actor = id.Subject
		tenantID = id.TenantID
	}
	audit.EmitBestEffort(ctx, logger, "backup", audit.AuditEvent{
		Actor:    actor,
		TenantID: tenantID,
		Resource: "backup:" + destination,
		Action:   "backup.create",
		Outcome:  audit.OutcomeSuccess,
		Attributes: map[string]any{
			"encryption": string(EncryptionNone),
			"plaintext":  true,
		},
	})
}

// NewBackupService creates a new BackupService backed by the given
// shard snapshot provider.
func NewBackupService(provider ShardSnapshotProvider, opts ...BackupServiceOption) BackupServicer {
	s := &BackupService{
		provider: provider,
		backups:  make(map[string]*backupEntry),
		idGen:    func() string { return uuid.New().String() },
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.keyWrap == nil {
		s.keyWrap = keywrap.NewDefaultRegistry()
	}
	if s.keyWrapExtra != nil {
		// Best-effort: a duplicate ID (e.g. an embedder re-registering
		// "software") is a composition-time configuration mistake the
		// embedder's own tests should catch, not something this
		// constructor can surface (BackupServiceOption returns nothing).
		_ = s.keyWrap.Register(s.keyWrapExtra.ProviderID(), func() (keywrap.Provider, error) {
			return s.keyWrapExtra, nil
		})
	}
	return s
}

// validCompressions is the set of valid compression types.
var validCompressions = map[CompressionType]struct{}{
	CompressionNone:   {},
	CompressionZstd:   {},
	CompressionSnappy: {},
}

// validEncryptions is the set of valid encryption types.
var validEncryptions = map[EncryptionType]struct{}{
	EncryptionNone:   {},
	EncryptionAES256: {},
}

// envelopeFormatVersion is the current BackupEnvelope format: AES-256-GCM
// at both the DEK-wrap and shard-payload layers, with AAD binding each
// shard to this backup's own metadata (buildShardAAD).
const envelopeFormatVersion uint32 = 1

// dekLength is the size, in bytes, of every backup's fresh per-backup
// data-encrypting key.
const dekLength = 32

// shardNonceLength is the AES-256-GCM nonce length used when encrypting
// each shard's payload under the backup's DEK.
const shardNonceLength = 12

// keyWrapRegistry returns the configured keywrap.Registry.
func (s *BackupService) keyWrapRegistry() keywrap.Registry {
	return s.keyWrap
}

// buildShardAAD derives the additional authenticated data AES-256-GCM
// binds a shard's ciphertext to: this backup's ID, destination, the
// specific shard ID this piece belongs to, and the envelope format
// version. Each field is length-prefixed so no ambiguity is possible
// across field boundaries. Recomputed identically at restore time from the
// backup's own trusted metadata -- never read back from the (until
// authenticated) stored artifact itself -- so a shard swapped in from a
// different backup, or between two shards of the same backup, fails
// authentication on Open rather than decrypting into the wrong place
// silently.
func buildShardAAD(backupID, destination string, shardID uint64, formatVersion uint32) []byte {
	var buf bytes.Buffer
	writeLenPrefixed(&buf, []byte(backupID))
	writeLenPrefixed(&buf, []byte(destination))
	var shardIDBuf [8]byte
	binary.BigEndian.PutUint64(shardIDBuf[:], shardID)
	writeLenPrefixed(&buf, shardIDBuf[:])
	var versionBuf [4]byte
	binary.BigEndian.PutUint32(versionBuf[:], formatVersion)
	writeLenPrefixed(&buf, versionBuf[:])
	return buf.Bytes()
}

// writeLenPrefixed appends a 4-byte big-endian length prefix followed by
// data to buf.
func writeLenPrefixed(buf *bytes.Buffer, data []byte) {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(data)))
	buf.Write(lenBuf[:])
	buf.Write(data)
}

// newShardGCM constructs an AES-256-GCM cipher.AEAD over dek.
func newShardGCM(dek []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// encryptShard seals plaintext under gcm, binding it to aad, and returns a
// fresh 12-byte nonce prepended to the ciphertext.
func encryptShard(gcm cipher.AEAD, aad, plaintext []byte) ([]byte, error) {
	nonce := make([]byte, shardNonceLength)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, aad), nil
}

// decryptShard splits stored into its nonce and ciphertext and opens it
// under gcm, checking aad. Returns *UnwrapFailedError-shaped failure
// (surfaced by callers as *BackupDecryptionFailedError) on a too-short
// payload or an authentication failure -- never distinguished from each
// other in what is returned.
func decryptShard(gcm cipher.AEAD, aad, stored []byte) ([]byte, error) {
	if len(stored) < shardNonceLength {
		return nil, &keywrap.UnwrapFailedError{}
	}
	nonce, ciphertext := stored[:shardNonceLength], stored[shardNonceLength:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, &keywrap.UnwrapFailedError{}
	}
	return plaintext, nil
}

// envelopeSetup is the result of preparing an encrypted backup's envelope
// before any shard is captured: a ready AEAD cipher to seal each shard's
// payload, and everything needed to persist the envelope once capture
// succeeds.
type envelopeSetup struct {
	gcm        cipher.AEAD
	providerID string
	keyRef     string
	wrappedDEK []byte
}

// prepareEnvelope resolves opts' key-wrap provider, generates a fresh DEK,
// and wraps it, returning a ready envelopeSetup. Called before any shard is
// captured, so a bad key reference or an unregistered provider is refused
// up front rather than after work has been done.
func (s *BackupService) prepareEnvelope(ctx context.Context, opts BackupOptions) (*envelopeSetup, error) {
	providerID := opts.KeyProviderID
	if providerID == "" {
		providerID = keywrap.SoftwareProviderID
	}

	provider, err := keywrap.Lookup(s.keyWrapRegistry(), providerID)
	if err != nil {
		return nil, &BackupDecryptionFailedError{Err: err}
	}

	dek := make([]byte, dekLength)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return nil, &BackupCreateError{Err: err}
	}
	defer secure.Wipe(dek)

	gcm, err := newShardGCM(dek)
	if err != nil {
		return nil, &BackupCreateError{Err: err}
	}

	wrappedDEK, err := provider.Wrap(ctx, opts.KeyRef, dek)
	if err != nil {
		return nil, &BackupDecryptionFailedError{Err: err}
	}

	return &envelopeSetup{
		gcm:        gcm,
		providerID: providerID,
		keyRef:     opts.KeyRef,
		wrappedDEK: wrappedDEK,
	}, nil
}

// CreateBackup creates a new backup by iterating shard snapshots.
func (s *BackupService) CreateBackup(ctx context.Context, opts BackupOptions) (*BackupInfo, error) {
	if opts.Destination == "" {
		return nil, &BackupEmptyDestinationError{}
	}

	compression := opts.Compression
	if compression == "" {
		compression = CompressionNone
	}
	if _, ok := validCompressions[compression]; !ok {
		return nil, &BackupInvalidCompressionError{Compression: string(compression)}
	}

	// A caller that names neither an encryption choice nor a key gets
	// refused, not a silently-minted key and not silent plaintext: the
	// default below is EncryptionAES256, so the very next check (missing
	// key) fires for it exactly as it would for an explicit "aes256" with
	// no key -- except the error names BOTH ways out (supply a key, or
	// say "none" outright), because this caller never asked for
	// encryption at all and needs telling that it is the default now.
	defaulted := opts.Encryption == ""
	encryption := opts.Encryption
	if defaulted {
		encryption = EncryptionAES256
	}
	if _, ok := validEncryptions[encryption]; !ok {
		return nil, &BackupInvalidEncryptionError{Encryption: string(encryption)}
	}

	if encryption != EncryptionNone && opts.KeyRef == "" {
		if defaulted {
			return nil, &BackupEncryptionRequiredError{}
		}
		return nil, &BackupEncryptionKeyRequiredError{}
	}

	if encryption == EncryptionNone {
		s.warnPlaintextBackup(ctx, opts.Destination)
	}

	// Resolve the key-wrap provider and wrap a fresh DEK BEFORE touching
	// any shard: a bad key reference or an unregistered provider is
	// refused up front, matching the "refused before any shard is
	// captured" invariant.
	var envSetup *envelopeSetup
	if encryption != EncryptionNone {
		var err error
		envSetup, err = s.prepareEnvelope(ctx, opts)
		if err != nil {
			return nil, err
		}
	}

	start := time.Now()
	id := s.idGen()

	info := BackupInfo{
		ID:          id,
		Timestamp:   start,
		Status:      BackupStatusRunning,
		Destination: opts.Destination,
		Compression: compression,
		Encryption:  encryption,
		Metadata:    opts.Metadata,
	}
	if envSetup != nil {
		info.KeyProviderID = envSetup.providerID
	}

	// Determine which shards to back up.
	shardIDs := opts.ShardIDs
	if len(shardIDs) == 0 {
		available, err := s.provider.AvailableShards(ctx)
		if err != nil {
			info.Status = BackupStatusFailed
			info.ErrorMessage = err.Error()
			info.Duration = time.Since(start)
			s.storeBackup(id, &backupEntry{info: info})
			return &info, &BackupCreateError{Err: err}
		}
		shardIDs = available
	}

	// Sort shard IDs for deterministic checksum computation.
	sort.Slice(shardIDs, func(i, j int) bool { return shardIDs[i] < shardIDs[j] })

	// Capture shard snapshots, encrypting each one under the backup's DEK
	// when encryption applies. The checksum covers exactly what is
	// stored -- ciphertext for an encrypted backup, plaintext for "none"
	// -- consistent with the checksum being an integrity (not
	// authenticity) property: Verify never needs key material.
	shardData := make(map[uint64][]byte, len(shardIDs))
	var totalSize int64
	hasher := sha256.New()

	for _, shardID := range shardIDs {
		if err := ctx.Err(); err != nil {
			info.Status = BackupStatusFailed
			info.ErrorMessage = err.Error()
			info.Duration = time.Since(start)
			s.storeBackup(id, &backupEntry{info: info})
			return &info, &BackupCreateError{Err: err}
		}

		data, err := s.provider.SnapshotShard(ctx, shardID)
		if err != nil {
			info.Status = BackupStatusFailed
			info.ErrorMessage = err.Error()
			info.Duration = time.Since(start)
			s.storeBackup(id, &backupEntry{info: info})
			return &info, &BackupCreateError{Err: err}
		}

		stored := data
		if envSetup != nil {
			aad := buildShardAAD(id, opts.Destination, shardID, envelopeFormatVersion)
			ciphertext, encErr := encryptShard(envSetup.gcm, aad, data)
			if encErr != nil {
				info.Status = BackupStatusFailed
				info.ErrorMessage = encErr.Error()
				info.Duration = time.Since(start)
				s.storeBackup(id, &backupEntry{info: info})
				return &info, &BackupCreateError{Err: encErr}
			}
			stored = ciphertext
		}

		shardData[shardID] = stored
		totalSize += int64(len(stored))
		// hasher.Write never returns an error per the hash.Hash contract.
		_, _ = hasher.Write(stored)
	}

	info.Size = totalSize
	info.ShardCount = len(shardData)
	info.Checksum = hex.EncodeToString(hasher.Sum(nil))
	info.Status = BackupStatusComplete
	info.Duration = time.Since(start)

	var envelope *BackupEnvelope
	if envSetup != nil {
		envelope = &BackupEnvelope{
			FormatVersion: envelopeFormatVersion,
			ProviderID:    envSetup.providerID,
			KeyRef:        envSetup.keyRef,
			WrappedDEK:    envSetup.wrappedDEK,
		}
	}

	// With a durable sink, persist shard data to the repository and keep only
	// metadata resident — the dataset is never pinned in process memory.
	if s.sink != nil {
		if err := s.sink.Write(ctx, info, shardData, envelope); err != nil {
			info.Status = BackupStatusFailed
			info.ErrorMessage = err.Error()
			info.Duration = time.Since(start)
			s.storeBackup(id, &backupEntry{info: info})
			return &info, &BackupCreateError{Err: err}
		}
		s.storeBackup(id, &backupEntry{info: info})
		return &info, nil
	}

	s.storeBackup(id, &backupEntry{info: info, shards: shardData, envelope: envelope})
	return &info, nil
}

// loadEntry returns a backup entry with its shard data (and envelope, when
// encrypted) populated. When the in-memory entry holds metadata only
// (sink-backed mode), the shard data and envelope are read from the sink on
// demand.
func (s *BackupService) loadEntry(ctx context.Context, id string) (*backupEntry, error) {
	s.mu.RLock()
	entry, ok := s.backups[id]
	s.mu.RUnlock()

	if ok && entry.shards != nil {
		return entry, nil
	}
	if s.sink == nil {
		if !ok {
			return nil, &BackupNotFoundError{ID: id}
		}
		return entry, nil
	}

	var info BackupInfo
	if ok {
		info = entry.info
	} else {
		pInfo, err := s.sink.ReadInfo(ctx, id)
		if err != nil {
			return nil, err
		}
		info = *pInfo
	}
	shards, err := s.sink.ReadShards(ctx, id)
	if err != nil {
		return nil, err
	}
	var envelope *BackupEnvelope
	if info.Encryption != EncryptionNone {
		envelope, err = s.sink.ReadEnvelope(ctx, id)
		if err != nil {
			return nil, err
		}
	}
	return &backupEntry{info: info, shards: shards, envelope: envelope}, nil
}

// ListBackups returns metadata for all available backups sorted by
// timestamp descending (newest first).
func (s *BackupService) ListBackups(ctx context.Context) ([]BackupInfo, error) {
	if s.sink != nil {
		return s.sink.List(ctx)
	}
	s.mu.RLock()
	result := make([]BackupInfo, 0, len(s.backups))
	for _, entry := range s.backups {
		result = append(result, entry.info)
	}
	s.mu.RUnlock()

	sort.Slice(result, func(i, j int) bool {
		return result[i].Timestamp.After(result[j].Timestamp)
	})
	return result, nil
}

// GetBackup returns metadata for a specific backup by ID.
func (s *BackupService) GetBackup(ctx context.Context, id string) (*BackupInfo, error) {
	if id == "" {
		return nil, &BackupEmptyIDError{}
	}
	s.mu.RLock()
	entry, ok := s.backups[id]
	s.mu.RUnlock()
	if ok {
		info := entry.info
		return &info, nil
	}
	if s.sink != nil {
		return s.sink.ReadInfo(ctx, id)
	}
	return nil, &BackupNotFoundError{ID: id}
}

// DeleteBackup removes a backup by ID.
func (s *BackupService) DeleteBackup(ctx context.Context, id string) error {
	if id == "" {
		return &BackupEmptyIDError{}
	}
	if s.sink != nil {
		s.mu.Lock()
		delete(s.backups, id)
		s.mu.Unlock()
		return s.sink.Delete(ctx, id)
	}
	s.mu.Lock()
	_, ok := s.backups[id]
	if !ok {
		s.mu.Unlock()
		return &BackupNotFoundError{ID: id}
	}
	delete(s.backups, id)
	s.mu.Unlock()
	return nil
}

// resolveDEK resolves entry's key-wrap provider and unwraps its DEK,
// returning a ready AEAD cipher for decrypting its shards. keyRefOverride,
// when non-empty, is used in place of the envelope's own stored KeyRef --
// the provider is always the one the envelope names; only the reference is
// overridable. Returns *BackupDecryptionFailedError on an unregistered
// provider or an Unwrap failure (wrong KEK or a tampered wrapped DEK),
// deliberately not distinguishing the two.
func (s *BackupService) resolveDEK(ctx context.Context, entry *backupEntry, keyRefOverride string) (cipher.AEAD, error) {
	envelope := entry.envelope
	if envelope == nil {
		return nil, &BackupDecryptionFailedError{BackupID: entry.info.ID, Err: &keywrap.UnwrapFailedError{}}
	}

	provider, err := keywrap.Lookup(s.keyWrapRegistry(), envelope.ProviderID)
	if err != nil {
		return nil, &BackupDecryptionFailedError{BackupID: entry.info.ID, Err: err}
	}

	keyRef := envelope.KeyRef
	if keyRefOverride != "" {
		keyRef = keyRefOverride
	}

	dek, err := provider.Unwrap(ctx, keyRef, envelope.WrappedDEK)
	if err != nil {
		return nil, &BackupDecryptionFailedError{BackupID: entry.info.ID, Err: err}
	}
	defer secure.Wipe(dek)

	gcm, err := newShardGCM(dek)
	if err != nil {
		return nil, &BackupDecryptionFailedError{BackupID: entry.info.ID, Err: err}
	}
	return gcm, nil
}

// RestoreBackup restores data from a backup. When ValidateFirst is set,
// the backup is verified before restoration begins.
func (s *BackupService) RestoreBackup(ctx context.Context, opts RestoreOptions) (*RestoreResult, error) {
	if opts.BackupID == "" {
		return nil, &BackupEmptyIDError{}
	}

	entry, err := s.loadEntry(ctx, opts.BackupID)
	if err != nil {
		return nil, err
	}

	start := time.Now()

	// Optional pre-restore verification.
	if opts.ValidateFirst {
		verifyResult, err := s.verifyEntry(entry)
		if err != nil {
			return nil, &BackupRestoreError{BackupID: opts.BackupID, Err: err}
		}
		if !verifyResult.Valid {
			return nil, &BackupChecksumMismatchError{
				Expected: verifyResult.ExpectedChecksum,
				Actual:   verifyResult.ActualChecksum,
			}
		}
	}

	// Resolve and unwrap the backup's DEK once, up front: a wrong KEK or an
	// unregistered provider cannot decrypt ANY shard (they all share the
	// same DEK), so it is a whole-restore failure before any shard is
	// touched -- unlike a single tampered shard's ciphertext, which is a
	// per-shard failure (see the loop below).
	var gcm cipher.AEAD
	if entry.info.Encryption != EncryptionNone {
		gcm, err = s.resolveDEK(ctx, entry, opts.DecryptionKeyRef)
		if err != nil {
			return nil, err
		}
	}

	// Determine which shards to restore.
	targetShards := opts.TargetShards
	if len(targetShards) == 0 {
		targetShards = make([]uint64, 0, len(entry.shards))
		for shardID := range entry.shards {
			targetShards = append(targetShards, shardID)
		}
	}

	var (
		restored atomic.Int32
		errs     []ShardError
		errMu    sync.Mutex
	)

	for _, shardID := range targetShards {
		if err := ctx.Err(); err != nil {
			return &RestoreResult{
				BackupID:       opts.BackupID,
				ShardsRestored: int(restored.Load()),
				Duration:       time.Since(start),
				Errors:         errs,
			}, &BackupRestoreError{BackupID: opts.BackupID, Err: err}
		}

		data, dataOK := entry.shards[shardID]
		if !dataOK {
			errMu.Lock()
			errs = append(errs, ShardError{
				ShardID: shardID,
				Message: "shard not found in backup",
			})
			errMu.Unlock()
			continue
		}

		plaintext := data
		if gcm != nil {
			aad := buildShardAAD(entry.info.ID, entry.info.Destination, shardID, entry.envelope.FormatVersion)
			decrypted, decErr := decryptShard(gcm, aad, data)
			if decErr != nil {
				errMu.Lock()
				errs = append(errs, ShardError{
					ShardID: shardID,
					Message: (&BackupDecryptionFailedError{BackupID: entry.info.ID, Err: decErr}).Error(),
				})
				errMu.Unlock()
				continue
			}
			plaintext = decrypted
		}

		// Prefer the SAFETY-GUARDED online path when the provider supports
		// it: the shard may still be RUNNING (this is the live REST/gRPC/CLI
		// restore path), and plain RestoreShard's ImportSnapshot always
		// refuses a running shard. A provider that does not implement
		// OnlineShardRestorer (e.g. a simple test double) falls back to
		// RestoreShard, which requires the shard to already be stopped —
		// exactly the OFFLINE boot-time restore pattern.
		restoreErr := s.restoreShard(ctx, shardID, plaintext)
		if restoreErr != nil {
			errMu.Lock()
			errs = append(errs, ShardError{
				ShardID: shardID,
				Message: restoreErr.Error(),
			})
			errMu.Unlock()
			continue
		}

		restored.Add(1)
	}

	result := &RestoreResult{
		BackupID:       opts.BackupID,
		ShardsRestored: int(restored.Load()),
		Duration:       time.Since(start),
		Errors:         errs,
	}

	return result, nil
}

// restoreShard restores a single shard, preferring the provider's
// SAFETY-GUARDED OnlineShardRestorer when available.
func (s *BackupService) restoreShard(ctx context.Context, shardID uint64, data []byte) error {
	if online, ok := s.provider.(OnlineShardRestorer); ok {
		return online.RestoreShardOnline(ctx, shardID, data)
	}
	return s.provider.RestoreShard(ctx, shardID, data)
}

// SetShardRestarter wires a ShardRestarter into svc's underlying host-backed
// ShardSnapshotProvider, enabling ONLINE restore (RestoreShardOnline) for the
// dedicated shards the restarter manages — e.g. server.RaftBackendFactory for
// its dedicated per-database Raft shards. Callers construct their
// IsolationRaft factory only after the core service graph (including the
// backup service returned by NewBackupService) already exists, so this is
// the late-binding wiring point: call it once the factory exists, before
// serving traffic. It is a safe no-op when svc is not a *BackupService (e.g.
// a test double), when its provider is not host-backed, or when restarter is
// nil.
func SetShardRestarter(svc BackupServicer, restarter ShardRestarter) {
	if restarter == nil {
		return
	}
	bs, ok := svc.(*BackupService)
	if !ok {
		return
	}
	p, ok := bs.provider.(*hostShardSnapshotProvider)
	if !ok {
		return
	}
	p.mu.Lock()
	p.restarter = restarter
	p.mu.Unlock()
}

// DecryptBackupShard returns shardID's plaintext from backup id, resolving
// and unwrapping the backup's envelope exactly as RestoreBackup does.
// decryptionKeyRef overrides the envelope's own stored key reference,
// exactly like RestoreOptions.DecryptionKeyRef; left empty, the stored
// reference is used.
//
// It exists for callers that must read and apply a single shard OUTSIDE
// RestoreBackup's own per-shard dispatch (which prefers
// OnlineShardRestorer.RestoreShardOnline and its live-restore safety
// guards) -- currently only qrdbd's offline, boot-time disaster-recovery
// restore (cmd/qrdbd/restore.go), which stops and restarts the shard
// around the import in a sequence RestoreBackup's own online dispatch does
// not perform. An EncryptionNone backup's shard is returned unchanged.
func (s *BackupService) DecryptBackupShard(ctx context.Context, id string, shardID uint64, decryptionKeyRef string) ([]byte, error) {
	if id == "" {
		return nil, &BackupEmptyIDError{}
	}

	entry, err := s.loadEntry(ctx, id)
	if err != nil {
		return nil, err
	}

	data, ok := entry.shards[shardID]
	if !ok {
		return nil, &BackupDecryptionFailedError{BackupID: id, Err: &keywrap.UnwrapFailedError{}}
	}

	if entry.info.Encryption == EncryptionNone {
		return data, nil
	}

	gcm, err := s.resolveDEK(ctx, entry, decryptionKeyRef)
	if err != nil {
		return nil, err
	}
	aad := buildShardAAD(entry.info.ID, entry.info.Destination, shardID, entry.envelope.FormatVersion)
	plaintext, err := decryptShard(gcm, aad, data)
	if err != nil {
		return nil, &BackupDecryptionFailedError{BackupID: id, Err: err}
	}
	return plaintext, nil
}

// VerifyBackup verifies the integrity of a backup by recomputing the
// checksum and comparing it to the stored value.
func (s *BackupService) VerifyBackup(ctx context.Context, id string) (*VerifyResult, error) {
	if id == "" {
		return nil, &BackupEmptyIDError{}
	}

	entry, err := s.loadEntry(ctx, id)
	if err != nil {
		return nil, err
	}

	return s.verifyEntry(entry)
}

// verifyEntry performs the actual checksum verification on a backup entry.
// The checksum covers exactly what is stored -- ciphertext for an
// encrypted backup -- so verification never needs key material: it is an
// integrity check (SHA-256), not an authenticity check (that is what
// decrypting at restore time confirms).
func (s *BackupService) verifyEntry(entry *backupEntry) (*VerifyResult, error) {
	hasher := sha256.New()

	// Compute checksum in deterministic shard order.
	shardIDs := make([]uint64, 0, len(entry.shards))
	for id := range entry.shards {
		shardIDs = append(shardIDs, id)
	}
	sort.Slice(shardIDs, func(i, j int) bool { return shardIDs[i] < shardIDs[j] })

	for _, shardID := range shardIDs {
		_, _ = hasher.Write(entry.shards[shardID])
	}
	actualChecksum := hex.EncodeToString(hasher.Sum(nil))

	var errs []string
	checksumMatch := actualChecksum == entry.info.Checksum
	if !checksumMatch {
		errs = append(errs, "checksum mismatch: expected "+entry.info.Checksum+", got "+actualChecksum)
	}

	if entry.info.Status == BackupStatusFailed {
		errs = append(errs, "backup status is failed: "+entry.info.ErrorMessage)
	}

	return &VerifyResult{
		Valid:            checksumMatch && entry.info.Status == BackupStatusComplete,
		ChecksumMatch:    checksumMatch,
		ExpectedChecksum: entry.info.Checksum,
		ActualChecksum:   actualChecksum,
		Errors:           errs,
	}, nil
}

// storeBackup safely stores a backup entry in the registry.
func (s *BackupService) storeBackup(id string, entry *backupEntry) {
	s.mu.Lock()
	s.backups[id] = entry
	s.mu.Unlock()
}

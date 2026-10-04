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
	"encoding/json"
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

	// SealedManifest is this backup's authenticated manifest: FormatVersion,
	// Algorithm, ProviderID, KeyRef, BackupID, Destination, and the exact
	// sorted set of shard IDs with a SHA-256 digest of each shard's stored
	// (ciphertext) bytes, JSON-encoded and sealed with AES-256-GCM under
	// this backup's own DEK (never the KEK). RestoreBackup and
	// DecryptBackupShard authenticate and open it before touching any
	// shard: a tampered header field, a dropped shard, or a substituted
	// shard all fail this check closed (go-qrdb security review,
	// backup-encrypt-default #B3). Empty for an EncryptionNone backup,
	// which carries no envelope at all.
	SealedManifest []byte
}

// backupManifestShard records one shard's identity and content digest
// inside a backup's authenticated manifest.
type backupManifestShard struct {
	ShardID uint64 `json:"shard_id"`
	Digest  string `json:"digest"` // hex-encoded SHA-256 of the stored (ciphertext) bytes
}

// backupManifest is a backup's authenticated header, sealed under its own
// DEK (see BackupEnvelope.SealedManifest): the envelope format version and
// algorithm, which KEK provider and key reference wrapped the DEK, the
// backup's own identity, and the exact sorted set of shards with a
// per-shard digest. Verified before importing any shard on restore --
// authenticating this binds every one of these fields and the shard set
// together, so none of them can be changed independently of the others
// without the DEK (go-qrdb security review, backup-encrypt-default #B3).
type backupManifest struct {
	FormatVersion uint32                `json:"format_version"`
	Algorithm     string                `json:"algorithm"`
	ProviderID    string                `json:"provider_id"`
	KeyRef        string                `json:"key_ref"`
	BackupID      string                `json:"backup_id"`
	Destination   string                `json:"destination"`
	Shards        []backupManifestShard `json:"shards"` // sorted by ShardID
}

// manifestAlgorithm is the sole algorithm backupManifest.Algorithm ever
// records for format version 1: AES-256-GCM at both the DEK-wrap and
// shard-payload layers, matching envelopeFormatVersion's own doc comment.
const manifestAlgorithm = "aes-256-gcm"

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
	// backup created with encryption explicitly disabled (EncryptionNone),
	// or a restore of one, emits an audit event recording that choice, in
	// addition to the WARN log every such backup/restore gets regardless.
	// A nil logger disables emission. It is read via
	// auditLogger()/SetAuditLogger under mu so a composer can attach it
	// after construction, the same accommodation
	// pkg/credentials.CredentialService makes for the same reason (the
	// seal subsystem builds services before the compliance logger
	// exists).
	audit audit.AuditLogger

	// auditFailurePolicy is this node's audit failure posture
	// (auth.audit_failure_policy). The plaintext-override audit record is
	// GATING -- it is emitted after the backup/restore has its ID but
	// before any shard data is touched -- so under the zero value,
	// FailClosed, a record that cannot be written refuses the
	// backup/restore rather than let an unrecorded plaintext override
	// through silently, the same posture every other gated audit site in
	// this repository holds (go-qrdb#445).
	auditFailurePolicy audit.FailurePolicy

	// keyDir is the directory the built-in "software" key-wrap provider's
	// key files must live inside, confined via *os.Root
	// (keywrap.NewSoftwareProvider). Consulted lazily, the first time
	// "software" is resolved through the default registry -- never used
	// when a caller supplies its own registry via WithKeyWrapRegistry.
	keyDir string
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

// WithAuditFailurePolicy sets this node's audit failure posture for the
// plaintext-override audit record (see the field's own doc comment).
// Defaults to the zero value, audit.FailClosed, when never applied.
func WithAuditFailurePolicy(p audit.FailurePolicy) BackupServiceOption {
	return func(s *BackupService) {
		s.auditFailurePolicy = p
	}
}

// WithKeyDir configures the directory the built-in "software" key-wrap
// provider's key files must live inside (see the field's own doc
// comment). Only consulted when no WithKeyWrapRegistry/WithKeyWrapProvider
// option supplies a registry of the caller's own.
func WithKeyDir(dir string) BackupServiceOption {
	return func(s *BackupService) {
		s.keyDir = dir
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

// SetAuditFailurePolicy installs this node's audit failure posture after
// construction, mirroring SetAuditLogger for the same boot-ordering
// reason.
func (s *BackupService) SetAuditFailurePolicy(p audit.FailurePolicy) {
	s.mu.Lock()
	s.auditFailurePolicy = p
	s.mu.Unlock()
}

// auditLogger returns the configured audit logger under mu.
func (s *BackupService) auditLogger() audit.AuditLogger {
	s.mu.RLock()
	l := s.audit
	s.mu.RUnlock()
	return l
}

// auditFailurePolicyValue returns the configured audit failure policy
// under mu.
func (s *BackupService) auditFailurePolicyValue() audit.FailurePolicy {
	s.mu.RLock()
	p := s.auditFailurePolicy
	s.mu.RUnlock()
	return p
}

// auditPlaintextOverride logs, at WARN, that a backup with id is being
// created or restored with encryption explicitly disabled, and records the
// same fact in the compliance audit trail -- GATING, not best-effort: it is
// called only for an EXPLICIT EncryptionNone (the default BackupOptions.
// Encryption, left empty, never reaches here, because CreateBackup refuses
// it before any backup starts -- BackupEncryptionRequiredError -- unless a
// key was also supplied, in which case it is encrypted, not plaintext), and
// it runs AFTER the backup/restore has its id (so the audit record names
// it) but BEFORE any shard data is captured or imported. Under this node's
// configured audit failure policy (the zero value, FailClosed, refuses),
// an audit record that cannot be written fails the backup/restore closed
// rather than let an unrecorded plaintext override through, matching every
// other gated audit site in this repository (go-qrdb#445). action is
// "backup.create" or "backup.restore".
func (s *BackupService) auditPlaintextOverride(ctx context.Context, id, destination, action string) error {
	slog.WarnContext(ctx, "service: backup "+action+" with encryption explicitly disabled; the backup is plaintext, and one covering the system shard carries the cluster CA and every tenant's login CA key material in the clear",
		"backup_id", id, "destination", destination)

	var actor, tenantID string
	if identity := auth.IdentityFromContext(ctx); identity != nil {
		actor = identity.Subject
		tenantID = identity.TenantID
	}
	return audit.EmitGated(ctx, s.auditLogger(), "backup", s.auditFailurePolicyValue(), audit.AuditEvent{
		Actor:    actor,
		TenantID: tenantID,
		Resource: "backup:" + id,
		Action:   action,
		Outcome:  audit.OutcomeSuccess,
		Attributes: map[string]any{
			"encryption":  string(EncryptionNone),
			"plaintext":   true,
			"destination": destination,
		},
	})
}

// NewBackupService creates a new BackupService backed by the given
// shard snapshot provider. Returns an error when WithKeyWrapProvider names
// a provider ID already present in the registry it is being installed
// into (the caller's own WithKeyWrapRegistry, or the default) --
// previously discarded, silently leaving the earlier registration in
// place (go-qrdb security review, backup-encrypt-default #B6).
func NewBackupService(provider ShardSnapshotProvider, opts ...BackupServiceOption) (BackupServicer, error) {
	s := &BackupService{
		provider: provider,
		backups:  make(map[string]*backupEntry),
		idGen:    func() string { return uuid.New().String() },
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.keyWrap == nil {
		// The "software" provider is resolved LAZILY, reading s.keyDir at
		// Lookup time rather than at construction: WithKeyDir and the
		// option loop above have already run by the time this closure is
		// built, but reading s.keyDir here (rather than capturing its
		// value now) keeps the one obvious extension point open for a
		// future SetKeyDir, exactly as audit/auditFailurePolicy already
		// work.
		s.keyWrap = keywrap.NewRegistry()
		_ = s.keyWrap.Register(keywrap.SoftwareProviderID, func() (keywrap.Provider, error) {
			if s.keyDir == "" {
				return nil, &BackupKeyDirNotConfiguredError{}
			}
			return keywrap.NewSoftwareProvider(s.keyDir)
		})
	}
	if s.keyWrapExtra != nil {
		if err := s.keyWrap.Register(s.keyWrapExtra.ProviderID(), func() (keywrap.Provider, error) {
			return s.keyWrapExtra, nil
		}); err != nil {
			return nil, err
		}
	}
	return s, nil
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
// under gcm, checking aad. Returns *BackupShardIntegrityError on a
// too-short payload or an authentication failure -- never distinguished
// from each other in what is returned, and deliberately a DIFFERENT type
// from keywrap.UnwrapFailedError: a shard's own ciphertext failing
// authentication has nothing to do with the KEK or the wrapped DEK, and
// reporting it as "wrong key-encrypting key" was misleading (go-qrdb
// security review, backup-encrypt-default).
func decryptShard(gcm cipher.AEAD, aad, stored []byte) ([]byte, error) {
	if len(stored) < shardNonceLength {
		return nil, &BackupShardIntegrityError{}
	}
	nonce, ciphertext := stored[:shardNonceLength], stored[shardNonceLength:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, &BackupShardIntegrityError{}
	}
	return plaintext, nil
}

// shardDigest returns the hex-encoded SHA-256 digest of a shard's stored
// (ciphertext, for an encrypted backup) bytes, as recorded in and verified
// against the backup's authenticated manifest.
func shardDigest(stored []byte) string {
	sum := sha256.Sum256(stored)
	return hex.EncodeToString(sum[:])
}

// buildManifestAAD derives the additional authenticated data the backup's
// manifest is sealed under: a domain-separation label (so a manifest
// ciphertext can never be mistaken for a shard ciphertext or vice versa),
// the backup's ID, its destination, and the envelope format version.
// Recomputed identically at restore time from the backup's own trusted
// metadata, exactly like buildShardAAD.
func buildManifestAAD(backupID, destination string, formatVersion uint32) []byte {
	var buf bytes.Buffer
	writeLenPrefixed(&buf, []byte("qrdb-backup-manifest"))
	writeLenPrefixed(&buf, []byte(backupID))
	writeLenPrefixed(&buf, []byte(destination))
	var versionBuf [4]byte
	binary.BigEndian.PutUint32(versionBuf[:], formatVersion)
	writeLenPrefixed(&buf, versionBuf[:])
	return buf.Bytes()
}

// sealManifest JSON-encodes manifest and seals it with AES-256-GCM under
// gcm (the backup's own DEK), binding it to aad, returning a fresh nonce
// prepended to the ciphertext -- the same shape encryptShard returns.
func sealManifest(gcm cipher.AEAD, manifest *backupManifest, aad []byte) ([]byte, error) {
	plain, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	return encryptShard(gcm, aad, plain)
}

// openManifest authenticates and decodes a backup's sealed manifest under
// gcm, checking aad. Returns *BackupManifestInvalidError -- carrying no
// further detail, for the same reason every other decryption failure in
// this package does not distinguish "wrong key" from "tampered" -- on
// authentication failure or malformed JSON.
func openManifest(gcm cipher.AEAD, sealed, aad []byte, backupID string) (*backupManifest, error) {
	plain, err := decryptShard(gcm, aad, sealed)
	if err != nil {
		return nil, &BackupManifestInvalidError{BackupID: backupID}
	}
	var manifest backupManifest
	if err := json.Unmarshal(plain, &manifest); err != nil {
		return nil, &BackupManifestInvalidError{BackupID: backupID}
	}
	return &manifest, nil
}

// buildManifestShards returns the sorted shard-digest list a backup's
// manifest records, one entry per shard in shardIDs (already sorted by
// the caller), each digest covering that shard's STORED bytes in
// shardData.
func buildManifestShards(shardIDs []uint64, shardData map[uint64][]byte) []backupManifestShard {
	shards := make([]backupManifestShard, len(shardIDs))
	for i, id := range shardIDs {
		shards[i] = backupManifestShard{ShardID: id, Digest: shardDigest(shardData[id])}
	}
	return shards
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

	start := time.Now()
	id := s.idGen()

	// The plaintext-override audit record is GATING, not best-effort: it
	// runs with the backup's own id, after validation but strictly before
	// any shard is touched, and -- under this node's configured audit
	// failure policy -- a record that cannot be written refuses the
	// backup rather than let an unrecorded plaintext override through
	// (go-qrdb security review, backup-encrypt-default #B5).
	if encryption == EncryptionNone {
		if err := s.auditPlaintextOverride(ctx, id, opts.Destination, "backup.create"); err != nil {
			return nil, err
		}
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
		manifest := &backupManifest{
			FormatVersion: envelopeFormatVersion,
			Algorithm:     manifestAlgorithm,
			ProviderID:    envSetup.providerID,
			KeyRef:        envSetup.keyRef,
			BackupID:      id,
			Destination:   opts.Destination,
			Shards:        buildManifestShards(shardIDs, shardData),
		}
		manifestAAD := buildManifestAAD(id, opts.Destination, envelopeFormatVersion)
		sealedManifest, sealErr := sealManifest(envSetup.gcm, manifest, manifestAAD)
		if sealErr != nil {
			info.Status = BackupStatusFailed
			info.ErrorMessage = sealErr.Error()
			info.Duration = time.Since(start)
			s.storeBackup(id, &backupEntry{info: info})
			return &info, &BackupCreateError{Err: sealErr}
		}
		envelope = &BackupEnvelope{
			FormatVersion:  envelopeFormatVersion,
			ProviderID:     envSetup.providerID,
			KeyRef:         envSetup.keyRef,
			WrappedDEK:     envSetup.wrappedDEK,
			SealedManifest: sealedManifest,
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

// checkEncryptionConsistency refuses when entry's stored metadata and its
// artifact disagree about whether the backup is encrypted -- an envelope
// present while the metadata claims EncryptionNone, or the metadata
// claiming otherwise with no envelope stored at all. Either shape means
// the metadata was edited independently of the cryptographic artifact: an
// attacker who flips BackupInfo.Encryption to "none" and substitutes
// plaintext still leaves the ORIGINAL envelope in the sink unless they
// also remove it, and this is what catches that (go-qrdb security
// review, backup-encrypt-default #B2).
func checkEncryptionConsistency(entry *backupEntry) error {
	claimsEncrypted := entry.info.Encryption != EncryptionNone
	hasEnvelope := entry.envelope != nil
	if claimsEncrypted != hasEnvelope {
		return &BackupMetadataMismatchError{BackupID: entry.info.ID}
	}
	return nil
}

// resolveManifest unwraps entry's DEK (resolveDEK) and authenticates its
// manifest, cross-checking the manifest's own backup ID, destination and
// provider against the trusted metadata it was read alongside. A
// mismatch means the envelope, the manifest, and the metadata were not
// all assembled together -- e.g. an envelope/manifest pair borrowed from
// a different backup under the same provider and key reference (go-qrdb
// security review, backup-encrypt-default #B2/#B3).
func (s *BackupService) resolveManifest(ctx context.Context, entry *backupEntry, keyRefOverride string) (cipher.AEAD, *backupManifest, error) {
	gcm, err := s.resolveDEK(ctx, entry, keyRefOverride)
	if err != nil {
		return nil, nil, err
	}
	envelope := entry.envelope
	aad := buildManifestAAD(entry.info.ID, entry.info.Destination, envelope.FormatVersion)
	manifest, err := openManifest(gcm, envelope.SealedManifest, aad, entry.info.ID)
	if err != nil {
		return nil, nil, err
	}
	if manifest.BackupID != entry.info.ID || manifest.Destination != entry.info.Destination || manifest.ProviderID != envelope.ProviderID {
		return nil, nil, &BackupMetadataMismatchError{BackupID: entry.info.ID}
	}
	return gcm, manifest, nil
}

// manifestDigests indexes manifest's shard-digest list by shard ID.
func manifestDigests(manifest *backupManifest) map[uint64]string {
	digests := make(map[uint64]string, len(manifest.Shards))
	for _, ms := range manifest.Shards {
		digests[ms.ShardID] = ms.Digest
	}
	return digests
}

// manifestShardIDs returns manifest's own recorded shard IDs -- the
// authenticated original shard set, which CreateBackup always records in
// sorted order.
func manifestShardIDs(manifest *backupManifest) []uint64 {
	ids := make([]uint64, len(manifest.Shards))
	for i, ms := range manifest.Shards {
		ids[i] = ms.ShardID
	}
	return ids
}

// verifyAndDecryptShard authenticates shardID's stored bytes against its
// recorded manifest digest and decrypts them under gcm, bound to the same
// AAD CreateBackup sealed them under. Returns *BackupShardNotFoundError
// when shardID has no stored payload or no manifest entry, and
// *BackupShardIntegrityError when its digest or its AEAD tag does not
// match. Shared by RestoreBackup (every target shard, before importing
// any) and DecryptBackupShard so both apply IDENTICAL checks.
func (s *BackupService) verifyAndDecryptShard(entry *backupEntry, digests map[uint64]string, gcm cipher.AEAD, shardID uint64) ([]byte, error) {
	data, ok := entry.shards[shardID]
	if !ok {
		return nil, &BackupShardNotFoundError{BackupID: entry.info.ID, ShardID: shardID}
	}
	wantDigest, inManifest := digests[shardID]
	if !inManifest {
		return nil, &BackupShardNotFoundError{BackupID: entry.info.ID, ShardID: shardID}
	}
	if shardDigest(data) != wantDigest {
		return nil, &BackupShardIntegrityError{}
	}
	aad := buildShardAAD(entry.info.ID, entry.info.Destination, shardID, entry.envelope.FormatVersion)
	return decryptShard(gcm, aad, data)
}

// RestoreBackup restores data from a backup. When ValidateFirst is set,
// the backup is verified before restoration begins.
//
// Every target shard is authenticated and decrypted (or, for an
// EncryptionNone backup, confirmed present) BEFORE any shard is imported.
// A tampered, substituted, or missing shard aborts the WHOLE restore with
// a typed top-level error and imports NOTHING -- a tampered shard never
// leaves an earlier shard already imported with no top-level error to
// show for it (go-qrdb security review, backup-encrypt-default #B1). Only
// an OPERATIONAL failure during the import itself (the underlying store
// refused it, after every shard already passed verification) is reported
// per-shard rather than aborting the shards that already succeeded.
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

	// B2: the stored metadata and the artifact must agree about whether
	// this backup is encrypted.
	if err := checkEncryptionConsistency(entry); err != nil {
		return nil, err
	}
	claimsEncrypted := entry.info.Encryption != EncryptionNone

	if !claimsEncrypted {
		if err := s.auditPlaintextOverride(ctx, opts.BackupID, entry.info.Destination, "backup.restore"); err != nil {
			return nil, err
		}
	}

	// Resolve and unwrap the backup's DEK, and authenticate its manifest,
	// once, up front: a wrong KEK, an unregistered provider, or a
	// tampered manifest cannot decrypt or authenticate ANY shard (they
	// all share the same DEK and the same manifest), so it is a
	// whole-restore failure before any shard is touched.
	var (
		gcm      cipher.AEAD
		manifest *backupManifest
		digests  map[uint64]string
	)
	if claimsEncrypted {
		gcm, manifest, err = s.resolveManifest(ctx, entry, opts.DecryptionKeyRef)
		if err != nil {
			return nil, err
		}
		digests = manifestDigests(manifest)
	}

	// Determine which shards to restore. For an encrypted backup with no
	// explicit target, the default is the MANIFEST's own recorded shard
	// set -- the authenticated original -- never the artifact's own
	// (possibly shrunk) shard map: a shard dropped from storage is then
	// caught by verification below instead of silently restoring fewer
	// shards with no error at all (go-qrdb security review,
	// backup-encrypt-default #B3).
	targetShards := opts.TargetShards
	if len(targetShards) == 0 {
		if manifest != nil {
			targetShards = manifestShardIDs(manifest)
		} else {
			targetShards = make([]uint64, 0, len(entry.shards))
			for shardID := range entry.shards {
				targetShards = append(targetShards, shardID)
			}
		}
	}

	// PASS 1: authenticate and decrypt (or, for plaintext, confirm the
	// presence of) EVERY target shard before importing any of them.
	plaintexts := make(map[uint64][]byte, len(targetShards))
	for _, shardID := range targetShards {
		if err := ctx.Err(); err != nil {
			return nil, &BackupRestoreError{BackupID: opts.BackupID, Err: err}
		}
		if manifest != nil {
			pt, verifyErr := s.verifyAndDecryptShard(entry, digests, gcm, shardID)
			if verifyErr != nil {
				return nil, &BackupVerificationFailedError{BackupID: opts.BackupID, ShardID: shardID, Err: verifyErr}
			}
			plaintexts[shardID] = pt
			continue
		}
		data, ok := entry.shards[shardID]
		if !ok {
			return nil, &BackupVerificationFailedError{
				BackupID: opts.BackupID,
				ShardID:  shardID,
				Err:      &BackupShardNotFoundError{BackupID: opts.BackupID, ShardID: shardID},
			}
		}
		plaintexts[shardID] = data
	}

	// PASS 2: import. Every target shard already passed verification in
	// pass 1; a failure here is an OPERATIONAL one (the underlying store
	// refused the import), reported per-shard rather than retroactively
	// undoing the shards that already succeeded.
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

		// Prefer the SAFETY-GUARDED online path when the provider supports
		// it: the shard may still be RUNNING (this is the live REST/gRPC/CLI
		// restore path), and plain RestoreShard's ImportSnapshot always
		// refuses a running shard. A provider that does not implement
		// OnlineShardRestorer (e.g. a simple test double) falls back to
		// RestoreShard, which requires the shard to already be stopped —
		// exactly the OFFLINE boot-time restore pattern.
		restoreErr := s.restoreShard(ctx, shardID, plaintexts[shardID])
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

// DecryptBackupShard returns shardID's plaintext from backup id, applying
// the SAME checks RestoreBackup applies to every target shard: the stored
// metadata and the artifact must agree about encryption
// (checkEncryptionConsistency), the manifest must authenticate and
// cross-check cleanly (resolveManifest), and shardID's own digest and AEAD
// tag must verify (verifyAndDecryptShard). decryptionKeyRef overrides the
// envelope's own stored key reference, exactly like
// RestoreOptions.DecryptionKeyRef; left empty, the stored reference is
// used.
//
// It exists for callers that must read and apply a single shard OUTSIDE
// RestoreBackup's own per-shard dispatch (which prefers
// OnlineShardRestorer.RestoreShardOnline and its live-restore safety
// guards) -- currently only qrdbd's offline, boot-time disaster-recovery
// restore (cmd/qrdbd/restore.go), which stops and restarts the shard
// around the import in a sequence RestoreBackup's own online dispatch does
// not perform. An EncryptionNone backup's shard is returned unchanged,
// after the same gated plaintext-restore audit RestoreBackup emits.
func (s *BackupService) DecryptBackupShard(ctx context.Context, id string, shardID uint64, decryptionKeyRef string) ([]byte, error) {
	if id == "" {
		return nil, &BackupEmptyIDError{}
	}

	entry, err := s.loadEntry(ctx, id)
	if err != nil {
		return nil, err
	}

	if err := checkEncryptionConsistency(entry); err != nil {
		return nil, err
	}

	if entry.info.Encryption == EncryptionNone {
		if err := s.auditPlaintextOverride(ctx, id, entry.info.Destination, "backup.restore"); err != nil {
			return nil, err
		}
		data, ok := entry.shards[shardID]
		if !ok {
			return nil, &BackupShardNotFoundError{BackupID: id, ShardID: shardID}
		}
		return data, nil
	}

	gcm, manifest, err := s.resolveManifest(ctx, entry, decryptionKeyRef)
	if err != nil {
		return nil, err
	}
	plaintext, err := s.verifyAndDecryptShard(entry, manifestDigests(manifest), gcm, shardID)
	if err != nil {
		return nil, err
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

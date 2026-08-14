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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
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
	EncryptionNone   EncryptionType = "none"
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
	Destination   string
	Compression   CompressionType
	Encryption    EncryptionType
	EncryptionKey []byte
	Metadata      map[string]string
	ShardIDs      []uint64
}

// BackupInfo contains metadata about a backup.
type BackupInfo struct {
	ID           string
	Timestamp    time.Time
	Size         int64
	ShardCount   int
	Status       BackupStatus
	Checksum     string
	Destination  string
	Compression  CompressionType
	Encryption   EncryptionType
	Metadata     map[string]string
	Duration     time.Duration
	ErrorMessage string
}

// RestoreOptions contains parameters for restoring a backup.
type RestoreOptions struct {
	BackupID      string
	TargetShards  []uint64
	ValidateFirst bool
	DecryptionKey []byte
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
	// Write persists a backup's metadata and shard data.
	Write(ctx context.Context, info BackupInfo, shards map[uint64][]byte) error

	// ReadInfo returns a backup's metadata without loading its shard data.
	ReadInfo(ctx context.Context, id string) (*BackupInfo, error)

	// ReadShards loads a backup's shard data.
	ReadShards(ctx context.Context, id string) (map[uint64][]byte, error)

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

// backupEntry stores backup metadata and data together.
type backupEntry struct {
	info   BackupInfo
	shards map[uint64][]byte
}

// BackupService implements BackupServicer using a ShardSnapshotProvider
// for shard data access and an in-memory backup registry.
type BackupService struct {
	provider ShardSnapshotProvider
	sink     BackupSink
	mu       sync.RWMutex
	backups  map[string]*backupEntry
	idGen    func() string
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

	encryption := opts.Encryption
	if encryption == "" {
		encryption = EncryptionNone
	}
	if _, ok := validEncryptions[encryption]; !ok {
		return nil, &BackupInvalidEncryptionError{Encryption: string(encryption)}
	}

	if encryption != EncryptionNone && len(opts.EncryptionKey) == 0 {
		return nil, &BackupEncryptionKeyRequiredError{}
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

	// Capture shard snapshots.
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

		shardData[shardID] = data
		totalSize += int64(len(data))
		// hasher.Write never returns an error per the hash.Hash contract.
		_, _ = hasher.Write(data)
	}

	info.Size = totalSize
	info.ShardCount = len(shardData)
	info.Checksum = hex.EncodeToString(hasher.Sum(nil))
	info.Status = BackupStatusComplete
	info.Duration = time.Since(start)

	// With a durable sink, persist shard data to the repository and keep only
	// metadata resident — the dataset is never pinned in process memory.
	if s.sink != nil {
		if err := s.sink.Write(ctx, info, shardData); err != nil {
			info.Status = BackupStatusFailed
			info.ErrorMessage = err.Error()
			info.Duration = time.Since(start)
			s.storeBackup(id, &backupEntry{info: info})
			return &info, &BackupCreateError{Err: err}
		}
		s.storeBackup(id, &backupEntry{info: info})
		return &info, nil
	}

	s.storeBackup(id, &backupEntry{info: info, shards: shardData})
	return &info, nil
}

// loadEntry returns a backup entry with its shard data populated. When the
// in-memory entry holds metadata only (sink-backed mode), the shard data is
// read from the sink on demand.
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
	return &backupEntry{info: info, shards: shards}, nil
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

	// Check if decryption key is needed.
	if entry.info.Encryption != EncryptionNone && len(opts.DecryptionKey) == 0 {
		return nil, &BackupDecryptionKeyRequiredError{}
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

		// Prefer the SAFETY-GUARDED online path when the provider supports
		// it: the shard may still be RUNNING (this is the live REST/gRPC/CLI
		// restore path), and plain RestoreShard's ImportSnapshot always
		// refuses a running shard. A provider that does not implement
		// OnlineShardRestorer (e.g. a simple test double) falls back to
		// RestoreShard, which requires the shard to already be stopped —
		// exactly the OFFLINE boot-time restore pattern.
		restoreErr := s.restoreShard(ctx, shardID, data)
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

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
	"bytes"
	"context"
	"strings"
)

// StagedRecords records one keyspace of a barrier's own storage backend that
// an encryption migration re-encrypts. Unlike a data store, these records sit
// in no directory the migration can swap: they share the backend with the
// root key and the migration journal. Each record under Prefix is
// re-encrypted under the target barrier to StagingPrefix+key in the same
// backend, and at commit the staged ciphertext is copied over the live key.
// The barrier binds no associated data to a record's ciphertext, so the copy
// needs no key and crash recovery can finish it from the journal alone.
type StagedRecords struct {
	// Prefix is the live keyspace, e.g. a standalone tenant's home store.
	Prefix string `json:"prefix"`
	// StagingPrefix is the migration-owned keyspace the re-encrypted copies
	// are written under, each at StagingPrefix + its live key.
	StagingPrefix string `json:"staging_prefix"`
}

// StageMigratedRecords re-encrypts every record under rec.Prefix from this
// barrier into target, writing each copy at rec.StagingPrefix + its key. It
// first discards whatever a prior interrupted attempt left in the staging
// keyspace, so it is idempotent. target must be a keyed sibling over this
// barrier's backend (MintKeyedSibling, MintKeyedShamirSibling), and both
// barriers must be unsealed. It returns the number of records staged.
func (b *StorageBarrier) StageMigratedRecords(ctx context.Context, target *StorageBarrier, rec StagedRecords) (int, error) {
	if err := RollbackStagedRecordsFrom(ctx, b.base, []StagedRecords{rec}); err != nil {
		return 0, err
	}
	keys, err := b.base.List(ctx, rec.Prefix)
	if err != nil {
		return 0, err
	}
	for _, key := range keys {
		plaintext, err := b.Get(ctx, key)
		if err != nil {
			return 0, err
		}
		err = target.Put(ctx, rec.StagingPrefix+key, plaintext)
		clear(plaintext)
		if err != nil {
			return 0, err
		}
	}
	return len(keys), nil
}

// VerifyMigratedRecords checks that every record under rec.Prefix has a
// staged copy that decrypts under target to the same plaintext. It returns
// ErrMigrationRecordMismatch when one is missing or differs.
func (b *StorageBarrier) VerifyMigratedRecords(ctx context.Context, target *StorageBarrier, rec StagedRecords) error {
	keys, err := b.base.List(ctx, rec.Prefix)
	if err != nil {
		return err
	}
	staged, err := b.base.List(ctx, rec.StagingPrefix)
	if err != nil {
		return err
	}
	if len(staged) != len(keys) {
		return ErrMigrationRecordMismatch
	}
	for _, key := range keys {
		if err := verifyMigratedRecord(ctx, b, target, key, rec.StagingPrefix+key); err != nil {
			return err
		}
	}
	return nil
}

// verifyMigratedRecord compares one live record read through source with its
// staged copy read through target.
func verifyMigratedRecord(ctx context.Context, source, target *StorageBarrier, key, stagedKey string) error {
	want, err := source.Get(ctx, key)
	if err != nil {
		return err
	}
	defer clear(want)
	got, err := target.Get(ctx, stagedKey)
	if err != nil {
		return ErrMigrationRecordMismatch
	}
	defer clear(got)
	if !bytes.Equal(got, want) {
		return ErrMigrationRecordMismatch
	}
	return nil
}

// CommitStagedRecordsFrom copies each staged record's ciphertext over its
// live key in backend and then deletes the staged copy. It works on the raw
// backend, without a barrier, so crash recovery can roll a committed
// migration forward before any barrier is built. It is idempotent: a record
// already committed has no staged copy left.
func CommitStagedRecordsFrom(ctx context.Context, backend StorageBackend, recs []StagedRecords) error {
	for _, rec := range recs {
		staged, err := backend.List(ctx, rec.StagingPrefix)
		if err != nil {
			return err
		}
		for _, stagedKey := range staged {
			blob, err := backend.Get(ctx, stagedKey)
			if err != nil {
				return err
			}
			if err := backend.Put(ctx, strings.TrimPrefix(stagedKey, rec.StagingPrefix), blob); err != nil {
				return err
			}
			if err := backend.Delete(ctx, stagedKey); err != nil && !isSecretNotFound(err) {
				return err
			}
		}
	}
	return nil
}

// RollbackStagedRecordsFrom deletes every staged record in backend, leaving
// the live keyspaces untouched. A missing record is not an error.
func RollbackStagedRecordsFrom(ctx context.Context, backend StorageBackend, recs []StagedRecords) error {
	for _, rec := range recs {
		staged, err := backend.List(ctx, rec.StagingPrefix)
		if err != nil {
			return err
		}
		for _, stagedKey := range staged {
			if err := backend.Delete(ctx, stagedKey); err != nil && !isSecretNotFound(err) {
				return err
			}
		}
	}
	return nil
}

// CommitStagedRecords is CommitStagedRecordsFrom over this barrier's backend.
func (b *StorageBarrier) CommitStagedRecords(ctx context.Context, recs []StagedRecords) error {
	return CommitStagedRecordsFrom(ctx, b.base, recs)
}

// RollbackStagedRecords is RollbackStagedRecordsFrom over this barrier's
// backend.
func (b *StorageBarrier) RollbackStagedRecords(ctx context.Context, recs []StagedRecords) error {
	return RollbackStagedRecordsFrom(ctx, b.base, recs)
}

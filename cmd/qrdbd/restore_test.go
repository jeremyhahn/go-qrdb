// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/service/backupsink"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
)

// TestRestoreShardFromBackup_RequiresBackupDir verifies that
// restoreShardFromBackup refuses --restore-backup-id when --backup-dir is
// unset, before touching the host at all (the function's very first check),
// so passing a nil host/createFn here is safe.
func TestRestoreShardFromBackup_RequiresBackupDir(t *testing.T) {
	f := &startFlags{restoreBackupID: "some-backup-id"}

	err := restoreShardFromBackup(context.Background(), f, nil, nil, quicraftconfig.Config{}, nil, audit.FailClosed)
	if err == nil {
		t.Fatal("restoreShardFromBackup: expected an error when --backup-dir is unset, got nil")
	}
	var srcErr *RestoreBackupSourceRequiredError
	if !errors.As(err, &srcErr) {
		t.Fatalf("restoreShardFromBackup: err = %v (%T), want *RestoreBackupSourceRequiredError", err, err)
	}
}

// TestRestoreShardFromBackup_MissingBackupFailsVerify verifies that a
// --restore-backup-id naming a backup that does not exist in the
// --backup-dir repository fails during the pre-restore verify step (before
// any shard bootstrap/stop/import), surfacing a wrapped RestoreBackupError
// rather than silently proceeding.
func TestRestoreShardFromBackup_MissingBackupFailsVerify(t *testing.T) {
	f := &startFlags{
		backupDir:       t.TempDir(),
		restoreBackupID: "does-not-exist",
	}

	err := restoreShardFromBackup(context.Background(), f, nil, nil, quicraftconfig.Config{ShardID: 1}, nil, audit.FailClosed)
	if err == nil {
		t.Fatal("restoreShardFromBackup: expected an error for an unknown backup ID, got nil")
	}
	var restoreErr *RestoreBackupError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("restoreShardFromBackup: err = %v (%T), want *RestoreBackupError", err, err)
	}
	if restoreErr.Phase != "verify" {
		t.Fatalf("restoreShardFromBackup: Phase = %q, want %q", restoreErr.Phase, "verify")
	}
}

// fixtureSnapshotProvider is a minimal service.ShardSnapshotProvider used
// only to seed a real, encrypted backup into a file-backed sink for
// TestRestoreShardFromBackup_DecryptFailureIsNotMisreportedAsShardMissing.
type fixtureSnapshotProvider struct {
	shardID uint64
	data    []byte
}

func (p *fixtureSnapshotProvider) AvailableShards(context.Context) ([]uint64, error) {
	return []uint64{p.shardID}, nil
}

func (p *fixtureSnapshotProvider) SnapshotShard(_ context.Context, shardID uint64) ([]byte, error) {
	if shardID != p.shardID {
		return nil, errors.New("unknown shard")
	}
	return p.data, nil
}

func (p *fixtureSnapshotProvider) RestoreShard(context.Context, uint64, []byte) error {
	return nil
}

// TestRestoreShardFromBackup_DecryptFailureIsNotMisreportedAsShardMissing
// proves a decrypt-phase failure OTHER than a genuinely missing shard --
// here, a tampered authenticated manifest (*service.
// BackupManifestInvalidError), which leaves the shard's own checksum
// untouched so pre-restore verification still passes -- surfaces as a
// *RestoreBackupError with Phase "decrypt", never the generic
// *RestoreBackupShardNotInArtifactError. Before this fix
// (cmd/qrdbd/restore.go:116, go-qrdb security review,
// backup-encrypt-default) EVERY DecryptBackupShard error other than
// *service.BackupDecryptionFailedError -- including this one -- was
// mapped to "shard not in artifact", even though the shard is genuinely
// present and merely failed authentication for an unrelated reason.
func TestRestoreShardFromBackup_DecryptFailureIsNotMisreportedAsShardMissing(t *testing.T) {
	backupDir := t.TempDir()
	keyDir := t.TempDir()
	const keyName = "backup.key"
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	if err := os.WriteFile(filepath.Join(keyDir, keyName), key, 0o600); err != nil {
		t.Fatalf("writing key file: %v", err)
	}

	backupStore, err := file.New(backupDir)
	if err != nil {
		t.Fatalf("file.New: %v", err)
	}
	defer func() { _ = backupStore.Close() }()

	sink := backupsink.New(backupStore)
	const shardID = uint64(1)
	provider := &fixtureSnapshotProvider{shardID: shardID, data: []byte("shard-plaintext")}
	backupSvc, err := service.NewBackupService(provider,
		service.WithBackupSink(sink),
		service.WithKeyDir(keyDir),
		service.WithIDGenerator(func() string { return "fixture-backup" }),
	)
	if err != nil {
		t.Fatalf("NewBackupService: %v", err)
	}
	if _, err := backupSvc.CreateBackup(context.Background(), service.BackupOptions{
		Destination: "test-fixture",
		KeyRef:      keyName,
		ShardIDs:    []uint64{shardID},
	}); err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Tamper the persisted envelope's manifest directly in the sink,
	// leaving the shard payload (and therefore its checksum) untouched:
	// VerifyBackup must still pass, so the failure this test exercises is
	// reached strictly at the decrypt phase.
	ctx := context.Background()
	info, err := sink.ReadInfo(ctx, "fixture-backup")
	if err != nil {
		t.Fatalf("ReadInfo: %v", err)
	}
	shards, err := sink.ReadShards(ctx, "fixture-backup")
	if err != nil {
		t.Fatalf("ReadShards: %v", err)
	}
	envelope, err := sink.ReadEnvelope(ctx, "fixture-backup")
	if err != nil {
		t.Fatalf("ReadEnvelope: %v", err)
	}
	envelope.SealedManifest[len(envelope.SealedManifest)-1] ^= 0xFF
	if err := sink.Write(ctx, *info, shards, envelope); err != nil {
		t.Fatalf("Write (re-persisting the tampered manifest): %v", err)
	}

	f := &startFlags{
		backupDir:       backupDir,
		restoreBackupID: "fixture-backup",
		backupKeyDir:    keyDir,
	}

	err = restoreShardFromBackup(context.Background(), f, nil, nil, quicraftconfig.Config{ShardID: shardID}, nil, audit.FailClosed)
	if err == nil {
		t.Fatal("restoreShardFromBackup: expected an error, got nil")
	}

	var notInArtifact *RestoreBackupShardNotInArtifactError
	if errors.As(err, &notInArtifact) {
		t.Fatalf("restoreShardFromBackup: err = %v, want a decrypt-phase *RestoreBackupError, NOT "+
			"*RestoreBackupShardNotInArtifactError -- the shard is genuinely present in the artifact", err)
	}
	var restoreErr *RestoreBackupError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("restoreShardFromBackup: err = %v (%T), want *RestoreBackupError", err, err)
	}
	if restoreErr.Phase != "decrypt" {
		t.Fatalf("restoreShardFromBackup: Phase = %q, want %q", restoreErr.Phase, "decrypt")
	}
}

// TestRestoreShardFromBackup_PlaintextRestoreAudited proves the offline,
// boot-time disaster-recovery restore path (restoreShardFromBackup) wires
// the compliance audit trail into the BackupService it builds, so a
// plaintext ("none") backup's restore is recorded through
// DecryptBackupShard's own gate+settle audit exactly like the live
// daemon's RestoreBackup/DecryptBackupShard calls are.
//
// Before this fix, restoreShardFromBackup built its BackupService with
// WithBackupSink/WithKeyDir and the key-wrap injection options but never
// WithAuditLogger/WithAuditFailurePolicy, although start.go already has
// the compliance trail open at this point in boot (it opens before the
// seal subsystem, and restoreShardFromBackup runs inside initializeNode,
// called after that) -- so a plaintext offline restore left NO audit
// record at all, unlike every other path into DecryptBackupShard.
func TestRestoreShardFromBackup_PlaintextRestoreAudited(t *testing.T) {
	backupDir := t.TempDir()
	backupStore, err := file.New(backupDir)
	if err != nil {
		t.Fatalf("file.New: %v", err)
	}
	defer func() { _ = backupStore.Close() }()

	sink := backupsink.New(backupStore)
	const shardID = uint64(1)
	provider := &fixtureSnapshotProvider{shardID: shardID, data: []byte("plaintext-shard")}
	seedSvc, err := service.NewBackupService(provider,
		service.WithBackupSink(sink),
		service.WithIDGenerator(func() string { return "fixture-backup" }),
	)
	if err != nil {
		t.Fatalf("NewBackupService: %v", err)
	}
	if _, err := seedSvc.CreateBackup(context.Background(), service.BackupOptions{
		Destination: "test-fixture-plaintext",
		Encryption:  service.EncryptionNone,
		ShardIDs:    []uint64{shardID},
	}); err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	ring := audit.NewRingBuffer(0)
	f := &startFlags{
		backupDir:       backupDir,
		restoreBackupID: "fixture-backup",
	}

	// restoreShardFromBackup itself fails at the bootstrap phase (a nil
	// host), but DecryptBackupShard -- and its audit gate+settle -- runs
	// strictly BEFORE that, so the trail already carries both records by
	// the time this returns.
	err = restoreShardFromBackup(context.Background(), f, nil, nil, quicraftconfig.Config{ShardID: shardID}, ring, audit.FailClosed)
	if err == nil {
		t.Fatal("restoreShardFromBackup: expected an error from the nil host at the bootstrap phase, got nil")
	}
	var restoreErr *RestoreBackupError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("restoreShardFromBackup: err = %v (%T), want *RestoreBackupError", err, err)
	}
	if restoreErr.Phase == "decrypt" {
		t.Fatalf("restoreShardFromBackup failed at the decrypt phase (%v): expected it to get past DecryptBackupShard", restoreErr.Err)
	}

	var restoreEvents int
	for _, ev := range ring.Snapshot() {
		if ev.Action == "backup.restore" {
			restoreEvents++
		}
	}
	if restoreEvents != 2 {
		t.Fatalf("got %d backup.restore audit events on the offline restore path, want exactly 2 (gate + settle)", restoreEvents)
	}
}

// fakeInjectedProvider is a same-shaped keywrap.Provider test double
// standing in for a go-xkms hardware/cloud-KMS provider, proving the
// INJECTION SEAM works -- never a real xkms type (go-qrdb never imports
// go-xkms). It XORs the DEK with a fixed, provider-held byte, which is
// enough to prove THIS instance (not the built-in "software" provider)
// was the one actually invoked.
type fakeInjectedProvider struct {
	id string
}

func (p *fakeInjectedProvider) ProviderID() string { return p.id }

func (p *fakeInjectedProvider) Wrap(_ context.Context, _ string, dek []byte) ([]byte, error) {
	out := make([]byte, len(dek))
	for i, b := range dek {
		out[i] = b ^ 0x5A
	}
	return out, nil
}

func (p *fakeInjectedProvider) Unwrap(_ context.Context, _ string, wrapped []byte) ([]byte, error) {
	out := make([]byte, len(wrapped))
	for i, b := range wrapped {
		out[i] = b ^ 0x5A
	}
	return out, nil
}

// TestRestoreShardFromBackup_UsesInjectedKeyWrapProvider proves the
// offline restore path (restoreShardFromBackup) resolves a backup's
// KeyProviderID through the SAME keyWrapInjection this daemon's live
// BuildServices call reads (start.go), not a hardcoded default registry
// of its own -- before this fix, restore.go built a bare
// service.NewBackupService with no way to receive an injected provider at
// all (go-qrdb security review, backup-encrypt-default #B6).
func TestRestoreShardFromBackup_UsesInjectedKeyWrapProvider(t *testing.T) {
	fake := &fakeInjectedProvider{id: "fake-hw"}
	prev := keyWrapInjection
	keyWrapInjection.provider = fake
	t.Cleanup(func() { keyWrapInjection = prev })

	backupDir := t.TempDir()
	backupStore, err := file.New(backupDir)
	if err != nil {
		t.Fatalf("file.New: %v", err)
	}
	defer func() { _ = backupStore.Close() }()

	sink := backupsink.New(backupStore)
	const shardID = uint64(1)
	plaintext := []byte("shard-plaintext-via-injected-provider")
	provider := &fixtureSnapshotProvider{shardID: shardID, data: plaintext}

	// Seed the backup with the SAME injected provider directly (standing
	// in for what the live daemon's BuildServices call would have done
	// with the identical keyWrapInjection.provider).
	seedSvc, err := service.NewBackupService(provider,
		service.WithBackupSink(sink),
		service.WithKeyWrapProvider(fake),
		service.WithIDGenerator(func() string { return "fixture-backup" }),
	)
	if err != nil {
		t.Fatalf("NewBackupService: %v", err)
	}
	if _, err := seedSvc.CreateBackup(context.Background(), service.BackupOptions{
		Destination:   "test-fixture-injected",
		KeyProviderID: "fake-hw",
		KeyRef:        "irrelevant-to-the-fake",
		ShardIDs:      []uint64{shardID},
	}); err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	f := &startFlags{
		backupDir:       backupDir,
		restoreBackupID: "fixture-backup",
	}

	// restoreShardFromBackup itself only reaches the point of returning
	// the decrypted shard bytes before touching a (nil, in this test)
	// quicraft.Host; capture that by making SnapshotShard's returned
	// bytes distinguishable and asserting the offline path got past
	// decrypt successfully to the bootstrap phase (where the nil host
	// finally surfaces), rather than failing at "decrypt" -- which is
	// exactly what happens if the fake provider is NOT resolved.
	err = restoreShardFromBackup(context.Background(), f, nil, nil, quicraftconfig.Config{ShardID: shardID}, nil, audit.FailClosed)
	if err == nil {
		t.Fatal("restoreShardFromBackup: expected an error from the nil host at the bootstrap phase, got nil")
	}
	var restoreErr *RestoreBackupError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("restoreShardFromBackup: err = %v (%T), want *RestoreBackupError", err, err)
	}
	if restoreErr.Phase == "decrypt" {
		t.Fatalf("restoreShardFromBackup failed at the decrypt phase (%v): the injected fake provider was not used", restoreErr.Err)
	}
}

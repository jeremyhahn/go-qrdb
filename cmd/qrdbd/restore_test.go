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
	"github.com/jeremyhahn/go-qrdb/pkg/keywrap"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/service/backupsink"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
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

// TestRestoreShardFromBackup_UnopenableBackupDir: a --backup-dir naming a
// regular file cannot hold a backup repository, and the restore fails at
// opening it, before any verify or shard work.
func TestRestoreShardFromBackup_UnopenableBackupDir(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}
	f := &startFlags{backupDir: notADir, restoreBackupID: "some-backup-id"}

	err := restoreShardFromBackup(context.Background(), f, nil, nil, quicraftconfig.Config{ShardID: 1}, nil, audit.FailClosed)
	var restoreErr *RestoreBackupError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("restoreShardFromBackup: err = %v (%T), want *RestoreBackupError", err, err)
	}
	if restoreErr.Phase != "open-backup-store" {
		t.Fatalf("restoreShardFromBackup: Phase = %q, want %q", restoreErr.Phase, "open-backup-store")
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

// seedPlaintextBackup writes a plaintext backup with the given ID into a
// file-backed sink at dir, holding data as shardID's payload.
func seedPlaintextBackup(t *testing.T, dir, backupID string, shardID uint64, data []byte) {
	t.Helper()
	store, err := file.New(dir)
	if err != nil {
		t.Fatalf("file.New: %v", err)
	}
	defer func() { _ = store.Close() }()
	svc, err := service.NewBackupService(&fixtureSnapshotProvider{shardID: shardID, data: data},
		service.WithBackupSink(backupsink.New(store)),
		service.WithIDGenerator(func() string { return backupID }),
	)
	if err != nil {
		t.Fatalf("NewBackupService: %v", err)
	}
	if _, err := svc.CreateBackup(context.Background(), service.BackupOptions{
		Destination: "test-fixture-plaintext",
		Encryption:  service.EncryptionNone,
		ShardIDs:    []uint64{shardID},
	}); err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
}

// TestRunStart_OfflineRestoreRefusesANonSnapshotArtifact drives the boot-time
// restore against a real host: the system shard is bootstrapped and stopped,
// and an artifact whose payload is not a snapshot fails the import, ending
// the boot before anything serves.
func TestRunStart_OfflineRestoreRefusesANonSnapshotArtifact(t *testing.T) {
	p, err := drawDaemonPorts()
	if err != nil {
		t.Fatalf("draw ports: %v", err)
	}
	f := fullStartFlags(t.TempDir(), p)
	f.backupDir = t.TempDir()
	f.restoreBackupID = "fixture-backup"
	f.allowPlaintextRestore = true
	seedPlaintextBackup(t, f.backupDir, f.restoreBackupID, shard.ShardKV, []byte("not-a-snapshot"))

	err = runStartUntilExit(t, f)
	var restoreErr *RestoreBackupError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("runStart = %v (%T), want *RestoreBackupError", err, err)
	}
	if restoreErr.Phase != "import" || restoreErr.ShardID != shard.ShardKV {
		t.Fatalf("RestoreBackupError = phase %q shard %d, want phase %q shard %d", restoreErr.Phase, restoreErr.ShardID, "import", shard.ShardKV)
	}
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
		backupDir:             backupDir,
		restoreBackupID:       "fixture-backup",
		allowPlaintextRestore: true,
	}

	// restoreShardFromBackup itself fails at the bootstrap phase (a nil
	// host), but the gate (and, via the deferred settle, the WHOLE
	// bracketed sequence's own outcome) now wraps decrypt THROUGH
	// restart, not merely DecryptBackupShard's own decrypt step (go-qrdb
	// security review, backup-encrypt-default round 4, B5 offline) --
	// so the settle record below must carry a FAILURE outcome, not
	// success, even though decrypt itself succeeded.
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

	var restoreEvents []audit.AuditEvent
	for _, ev := range ring.Snapshot() {
		if ev.Action == "backup.restore" {
			restoreEvents = append(restoreEvents, ev)
		}
	}
	if len(restoreEvents) != 2 {
		t.Fatalf("got %d backup.restore audit events on the offline restore path, want exactly 2 (gate + settle)", len(restoreEvents))
	}
	if restoreEvents[0].Outcome != audit.OutcomePermitted {
		t.Fatalf("gate event Outcome = %q, want %q", restoreEvents[0].Outcome, audit.OutcomePermitted)
	}
	// The OUTCOME, not merely the count: the bracketed sequence failed
	// (at bootstrap, a nil host), so the settle record must say so --
	// never OutcomeSuccess for a restore that did not complete.
	if restoreEvents[1].Outcome == audit.OutcomeSuccess {
		t.Fatalf("settle event Outcome = %q, want a failure outcome: the overall restore sequence failed at phase %q", restoreEvents[1].Outcome, restoreErr.Phase)
	}
}

// TestRestoreShardFromBackup_PlaintextRefusedWithoutOptIn proves the
// offline restore path refuses a plaintext backup with a typed error
// when --allow-plaintext-restore is not set, before anything else runs
// (no bootstrap, no audit gate -- nothing happened, so nothing is
// recorded).
func TestRestoreShardFromBackup_PlaintextRefusedWithoutOptIn(t *testing.T) {
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
		service.WithIDGenerator(func() string { return "fixture-backup-no-optin" }),
	)
	if err != nil {
		t.Fatalf("NewBackupService: %v", err)
	}
	if _, err := seedSvc.CreateBackup(context.Background(), service.BackupOptions{
		Destination: "test-fixture-plaintext-no-optin",
		Encryption:  service.EncryptionNone,
		ShardIDs:    []uint64{shardID},
	}); err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	ring := audit.NewRingBuffer(0)
	f := &startFlags{
		backupDir:       backupDir,
		restoreBackupID: "fixture-backup-no-optin",
		// allowPlaintextRestore left false: the default.
	}

	err = restoreShardFromBackup(context.Background(), f, nil, nil, quicraftconfig.Config{ShardID: shardID}, ring, audit.FailClosed)
	if err == nil {
		t.Fatal("restoreShardFromBackup: expected an error for a plaintext backup with no --allow-plaintext-restore")
	}
	var restoreErr *RestoreBackupError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("restoreShardFromBackup: err = %v (%T), want *RestoreBackupError", err, err)
	}
	var notAllowed *service.BackupPlaintextRestoreNotAllowedError
	if !errors.As(restoreErr, &notAllowed) {
		t.Fatalf("restoreShardFromBackup: err = %v, want it to wrap *service.BackupPlaintextRestoreNotAllowedError", err)
	}
	if len(ring.Snapshot()) != 0 {
		t.Fatalf("refused before the gate ran: expected 0 audit events, got %d", len(ring.Snapshot()))
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

// TestRestoreShardFromBackup_UsesInjectedKeyWrapRegistry is the registry half
// of TestRestoreShardFromBackup_UsesInjectedKeyWrapProvider: an embedder that
// injects a keywrap.Registry, rather than a single provider, has the offline
// restore resolve the backup's KeyProviderID through that registry too.
func TestRestoreShardFromBackup_UsesInjectedKeyWrapRegistry(t *testing.T) {
	fake := &fakeInjectedProvider{id: "fake-hw"}
	reg := keywrap.NewRegistry()
	if err := reg.Register("fake-hw", func() (keywrap.Provider, error) { return fake, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	prev := keyWrapInjection
	keyWrapInjection.registry = reg
	t.Cleanup(func() { keyWrapInjection = prev })

	backupDir := t.TempDir()
	backupStore, err := file.New(backupDir)
	if err != nil {
		t.Fatalf("file.New: %v", err)
	}
	defer func() { _ = backupStore.Close() }()

	const shardID = uint64(1)
	seedSvc, err := service.NewBackupService(&fixtureSnapshotProvider{shardID: shardID, data: []byte("shard-plaintext-via-injected-registry")},
		service.WithBackupSink(backupsink.New(backupStore)),
		service.WithKeyWrapRegistry(reg),
		service.WithIDGenerator(func() string { return "fixture-backup" }),
	)
	if err != nil {
		t.Fatalf("NewBackupService: %v", err)
	}
	if _, err := seedSvc.CreateBackup(context.Background(), service.BackupOptions{
		Destination:   "test-fixture-injected-registry",
		KeyProviderID: "fake-hw",
		KeyRef:        "irrelevant-to-the-fake",
		ShardIDs:      []uint64{shardID},
	}); err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	f := &startFlags{backupDir: backupDir, restoreBackupID: "fixture-backup"}
	err = restoreShardFromBackup(context.Background(), f, nil, nil, quicraftconfig.Config{ShardID: shardID}, nil, audit.FailClosed)
	var restoreErr *RestoreBackupError
	if !errors.As(err, &restoreErr) {
		t.Fatalf("restoreShardFromBackup: err = %v (%T), want *RestoreBackupError from the nil host", err, err)
	}
	if restoreErr.Phase == "decrypt" {
		t.Fatalf("restoreShardFromBackup failed at the decrypt phase (%v): the injected registry was not used", restoreErr.Err)
	}
}

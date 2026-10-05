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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/keywrap"
)

// This file proves the specific defects go-qrdb's security review of
// backup-encrypt-default found (B1-B6), each with a test that fails
// without the corresponding fix.

// refusingAuditLogger refuses every record, as a full disk refuses the
// compliance audit trail -- the same local double
// pkg/ceremony/holder_audit_test.go uses for the identical reason.
type refusingAuditLogger struct {
	err   error
	calls int
}

var _ audit.AuditLogger = (*refusingAuditLogger)(nil)

func (l *refusingAuditLogger) Emit(context.Context, audit.AuditEvent) error {
	l.calls++
	return l.err
}

func (l *refusingAuditLogger) Query(context.Context, audit.Filter) (*audit.Page, error) {
	return &audit.Page{}, nil
}

func (l *refusingAuditLogger) Close() error { return nil }

// ---------------------------------------------------------------------------
// B6: injection -- the discarded duplicate-registration error
// ---------------------------------------------------------------------------

// TestNewBackupService_DuplicateKeyWrapProviderReturnsTypedError proves a
// WithKeyWrapProvider collision with an already-registered ID is returned,
// typed, from NewBackupService -- not silently discarded (the review's
// finding at the old :458). Reverting NewBackupService to its single-value
// signature (ignoring the Register error) makes this test fail to compile;
// reverting only the propagation (keeping `_ = s.keyWrap.Register(...)`)
// makes it fail at the nil check below.
func TestNewBackupService_DuplicateKeyWrapProviderReturnsTypedError(t *testing.T) {
	registry := keywrap.NewRegistry()
	fake := &fakeKeyWrapProvider{id: "software"}
	if err := registry.Register("software", func() (keywrap.Provider, error) { return fake, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}

	_, err := NewBackupService(newMockSnapshotProvider(),
		WithKeyWrapRegistry(registry),
		WithKeyWrapProvider(fake), // same ID, already registered above
	)
	if err == nil {
		t.Fatal("NewBackupService succeeded despite a duplicate key-wrap provider registration")
	}
	var dup *keywrap.ProviderAlreadyRegisteredError
	if !errors.As(err, &dup) {
		t.Fatalf("err = %T, want *keywrap.ProviderAlreadyRegisteredError", err)
	}
}

// ---------------------------------------------------------------------------
// B4 (service integration): no key directory configured
// ---------------------------------------------------------------------------

// TestBackupService_CreateBackup_NoKeyDirConfigured_Refused proves that
// using the built-in "software" provider with NO WithKeyDir configured
// refuses with the typed *BackupKeyDirNotConfiguredError, rather than
// resolving the provider against an implicit directory (e.g. the process's
// current working directory).
func TestBackupService_CreateBackup_NoKeyDirConfigured_Refused(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc, err := NewBackupService(provider) // deliberately NOT mustNewBackupService: no WithKeyDir
	if err != nil {
		t.Fatalf("NewBackupService: %v", err)
	}

	_, err = svc.CreateBackup(context.Background(), BackupOptions{
		Destination: "/backups/no-key-dir",
		KeyRef:      "backup.key",
	})
	if err == nil {
		t.Fatal("CreateBackup succeeded with no key directory configured")
	}
	var noDir *BackupKeyDirNotConfiguredError
	if !errors.As(err, &noDir) {
		t.Fatalf("err = %v, want it to wrap *BackupKeyDirNotConfiguredError", err)
	}
}

// ---------------------------------------------------------------------------
// B2: downgrade -- metadata and artifact must agree about encryption
// ---------------------------------------------------------------------------

// TestBackupService_RestoreBackup_MetaNoneButEnvelopePresent_Refused
// simulates the review's attack: an attacker flips the stored metadata's
// Encryption to "none" while the original envelope (which they cannot
// forge without the KEK) survives. RestoreBackup must refuse before
// reading anything else.
func TestBackupService_RestoreBackup_MetaNoneButEnvelopePresent_Refused(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)

	ctx := context.Background()
	created, err := svc.CreateBackup(ctx, BackupOptions{
		Destination: "/backups/downgrade-attempt",
		KeyRef:      writeTestKeyFile(t, 0xC1),
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	bs := svc.(*BackupService)
	bs.mu.Lock()
	entry := bs.backups[created.ID]
	entry.info.Encryption = EncryptionNone // the attacker's edit; entry.envelope is untouched
	bs.mu.Unlock()

	result, err := svc.RestoreBackup(ctx, RestoreOptions{BackupID: created.ID})
	if err == nil {
		t.Fatal("RestoreBackup succeeded despite metadata claiming \"none\" while an envelope is present")
	}
	if result != nil {
		t.Fatalf("expected a nil result alongside the refusal, got %+v", result)
	}
	var mismatch *BackupMetadataMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("err = %T, want *BackupMetadataMismatchError", err)
	}
	if provider.restoreCalls.Load() != 0 {
		t.Fatal("a refused downgrade restore must not have touched any shard")
	}
}

// TestBackupService_RestoreBackup_MetaEncryptedButNoEnvelope_Refused is the
// mirror case: metadata claims encryption but the entry carries no
// envelope at all.
func TestBackupService_RestoreBackup_MetaEncryptedButNoEnvelope_Refused(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)

	ctx := context.Background()
	created, err := svc.CreateBackup(ctx, BackupOptions{
		Destination: "/backups/missing-envelope",
		Encryption:  EncryptionNone,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	bs := svc.(*BackupService)
	bs.mu.Lock()
	entry := bs.backups[created.ID]
	entry.info.Encryption = EncryptionAES256 // claims encrypted; entry.envelope stays nil
	bs.mu.Unlock()

	_, err = svc.RestoreBackup(ctx, RestoreOptions{BackupID: created.ID})
	var mismatch *BackupMetadataMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("err = %T, want *BackupMetadataMismatchError", err)
	}
}

// ---------------------------------------------------------------------------
// B3: the authenticated manifest
// ---------------------------------------------------------------------------

// TestBackupService_RestoreBackup_TamperedManifestRefused proves a bit
// flip in the sealed manifest itself (distinct from a shard's own
// ciphertext) is caught before any shard is touched.
func TestBackupService_RestoreBackup_TamperedManifestRefused(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)

	ctx := context.Background()
	created, err := svc.CreateBackup(ctx, BackupOptions{
		Destination: "/backups/manifest-tamper",
		KeyRef:      writeTestKeyFile(t, 0xC2),
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	bs := svc.(*BackupService)
	bs.mu.Lock()
	entry := bs.backups[created.ID]
	sealed := entry.envelope.SealedManifest
	sealed[len(sealed)-1] ^= 0xFF
	bs.mu.Unlock()

	result, err := svc.RestoreBackup(ctx, RestoreOptions{BackupID: created.ID})
	if err == nil {
		t.Fatal("RestoreBackup succeeded despite a tampered manifest")
	}
	if result != nil {
		t.Fatalf("expected a nil result alongside the refusal, got %+v", result)
	}
	var invalid *BackupManifestInvalidError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %T, want *BackupManifestInvalidError", err)
	}
	if provider.restoreCalls.Load() != 0 {
		t.Fatal("a tampered manifest must leave nothing imported")
	}
}

// TestBackupService_RestoreBackup_DroppedShardDetected proves a shard
// recorded in the manifest but absent from the artifact (e.g. deleted from
// a durable sink by an attacker, or lost to corruption) aborts the default
// (no explicit TargetShards) restore -- it must NOT silently restore the
// remaining shards and report total success.
func TestBackupService_RestoreBackup_DroppedShardDetected(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)

	ctx := context.Background()
	created, err := svc.CreateBackup(ctx, BackupOptions{
		Destination: "/backups/dropped-shard",
		KeyRef:      writeTestKeyFile(t, 0xC3),
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	bs := svc.(*BackupService)
	bs.mu.Lock()
	entry := bs.backups[created.ID]
	var droppedID uint64
	for id := range entry.shards {
		droppedID = id
		break
	}
	delete(entry.shards, droppedID)
	remaining := len(entry.shards)
	bs.mu.Unlock()

	result, err := svc.RestoreBackup(ctx, RestoreOptions{BackupID: created.ID})
	if err == nil {
		t.Fatalf("RestoreBackup succeeded despite a shard dropped from storage (manifest still records %d shards, artifact has %d)",
			remaining+1, remaining)
	}
	if result != nil {
		t.Fatalf("expected a nil result alongside the refusal, got %+v", result)
	}
	var verifyErr *BackupVerificationFailedError
	if !errors.As(err, &verifyErr) {
		t.Fatalf("err = %T, want *BackupVerificationFailedError", err)
	}
	if verifyErr.ShardID != droppedID {
		t.Fatalf("verifyErr.ShardID = %d, want the dropped shard %d", verifyErr.ShardID, droppedID)
	}
	if provider.restoreCalls.Load() != 0 {
		t.Fatal("a restore that detects a dropped shard must import nothing, including the shards still present")
	}
}

// ---------------------------------------------------------------------------
// B2 (bullet 3) / B5: the plaintext-restore audit record, and gating
// ---------------------------------------------------------------------------

// TestBackupService_RestoreBackup_PlaintextRestoreAudited proves restoring
// a plaintext backup emits the SAME shape of audit record CreateBackup's
// plaintext override does, with action "backup.restore".
func TestBackupService_RestoreBackup_PlaintextRestoreAudited(t *testing.T) {
	ring := audit.NewRingBuffer(0)
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider, WithAuditLogger(ring))

	ctx := context.Background()
	created, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/backups/plaintext-restore-audit", Encryption: EncryptionNone})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	if _, err := svc.RestoreBackup(ctx, RestoreOptions{BackupID: created.ID}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	// Two records: a GATING "permitted" record before the restore ran,
	// then a post-hoc record carrying the real outcome (go-qrdb security
	// review, backup-encrypt-default #B5).
	var restoreEvents []audit.AuditEvent
	for _, ev := range ring.Snapshot() {
		if ev.Action == "backup.restore" {
			restoreEvents = append(restoreEvents, ev)
			if ev.Resource != "backup:"+created.ID {
				t.Fatalf("restore audit Resource = %q, want %q", ev.Resource, "backup:"+created.ID)
			}
		}
	}
	if len(restoreEvents) != 2 {
		t.Fatalf("got %d backup.restore audit events, want exactly 2 (gate + settle)", len(restoreEvents))
	}
	if restoreEvents[0].Outcome != audit.OutcomePermitted {
		t.Fatalf("restore gate event Outcome = %q, want %q", restoreEvents[0].Outcome, audit.OutcomePermitted)
	}
	if restoreEvents[1].Outcome != audit.OutcomeSuccess {
		t.Fatalf("restore settle event Outcome = %q, want %q", restoreEvents[1].Outcome, audit.OutcomeSuccess)
	}
}

// TestBackupService_CreateBackup_AuditWriteFailureFailsClosed proves that
// under the default (zero-value) FailClosed posture, a plaintext backup
// whose audit record cannot be written is refused -- and nothing is
// stored -- rather than created with an unrecorded override (go-qrdb
// security review, backup-encrypt-default #B5).
func TestBackupService_CreateBackup_AuditWriteFailureFailsClosed(t *testing.T) {
	logger := &refusingAuditLogger{err: errors.New("disk full")}
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider, WithAuditLogger(logger)) // FailClosed is the zero value

	_, err := svc.CreateBackup(context.Background(), BackupOptions{
		Destination: "/backups/audit-fail-closed",
		Encryption:  EncryptionNone,
	})
	if err == nil {
		t.Fatal("CreateBackup succeeded despite an unrecordable plaintext-override audit event")
	}
	var gateErr *audit.GateError
	if !errors.As(err, &gateErr) {
		t.Fatalf("err = %T, want *audit.GateError", err)
	}
	if logger.calls != 1 {
		t.Fatalf("audit logger called %d times, want exactly 1", logger.calls)
	}

	list, err := svc.ListBackups(context.Background())
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("a backup whose gating audit record failed under FailClosed must not be stored, got %d", len(list))
	}
}

// TestBackupService_CreateBackup_AuditWriteFailureFailOpenProceeds proves
// the complementary posture: FailOpen lets the plaintext override through
// despite the unrecordable audit event, as every other gated audit site in
// this repository does under the same policy (go-qrdb#445).
func TestBackupService_CreateBackup_AuditWriteFailureFailOpenProceeds(t *testing.T) {
	logger := &refusingAuditLogger{err: errors.New("disk full")}
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider, WithAuditLogger(logger), WithAuditFailurePolicy(audit.FailOpen))

	info, err := svc.CreateBackup(context.Background(), BackupOptions{
		Destination: "/backups/audit-fail-open",
		Encryption:  EncryptionNone,
	})
	if err != nil {
		t.Fatalf("CreateBackup under FailOpen: %v", err)
	}
	if info.Status != BackupStatusComplete {
		t.Fatalf("status = %s, want complete", info.Status)
	}
	// Two Emit calls: the GATING record (FailOpen swallows its own
	// refusal and proceeds) and the post-hoc settle record carrying the
	// real (success) outcome, each independently refused by this logger
	// and each counted.
	if logger.calls != 2 {
		t.Fatalf("audit logger called %d times, want exactly 2 (gate + settle)", logger.calls)
	}
}

// TestBackupService_CreateBackup_FailedPlaintextBackupAuditsAsFailure
// proves the real outcome -- not a success recorded before the backup ran
// at all -- is what lands on the compliance trail: a plaintext backup
// whose shard capture fails emits a GATING "permitted" record followed by
// a "failure" settle record, never "success" (go-qrdb security review,
// backup-encrypt-default #B5). Before the gate/settle split, the single
// audit record this function emitted was written BEFORE CreateBackup ever
// touched a shard and unconditionally said OutcomeSuccess, so a backup
// that failed immediately afterward still left a trail saying it
// succeeded.
func TestBackupService_CreateBackup_FailedPlaintextBackupAuditsAsFailure(t *testing.T) {
	ring := audit.NewRingBuffer(0)
	provider := newMockSnapshotProvider()
	provider.snapshotErr = errors.New("simulated shard snapshot failure")
	svc := mustNewBackupService(t, provider, WithAuditLogger(ring))

	_, err := svc.CreateBackup(context.Background(), BackupOptions{
		Destination: "/backups/plaintext-fails",
		Encryption:  EncryptionNone,
	})
	if err == nil {
		t.Fatal("CreateBackup succeeded despite a forced SnapshotShard failure")
	}
	var createErr *BackupCreateError
	if !errors.As(err, &createErr) {
		t.Fatalf("err = %T, want *BackupCreateError", err)
	}

	events := ring.Snapshot()
	if len(events) != 2 {
		t.Fatalf("audit log has %d events, want exactly 2 (gate + settle)", len(events))
	}
	gate, settle := events[0], events[1]
	if gate.Outcome != audit.OutcomePermitted {
		t.Fatalf("gate event Outcome = %q, want %q", gate.Outcome, audit.OutcomePermitted)
	}
	if settle.Outcome == audit.OutcomeSuccess {
		t.Fatalf("settle event Outcome = %q, want a failure outcome: the backup did not succeed", settle.Outcome)
	}
	wantPrefix := audit.OutcomeFailure + ":"
	if len(settle.Outcome) < len(wantPrefix) || settle.Outcome[:len(wantPrefix)] != wantPrefix {
		t.Fatalf("settle event Outcome = %q, want prefix %q", settle.Outcome, wantPrefix)
	}
}

// TestBackupService_SetAuditFailurePolicy_AppliesAfterConstruction proves
// SetAuditFailurePolicy (the post-construction setter, mirroring
// SetAuditLogger) takes effect on the next gated call.
func TestBackupService_SetAuditFailurePolicy_AppliesAfterConstruction(t *testing.T) {
	logger := &refusingAuditLogger{err: errors.New("disk full")}
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)
	bs := svc.(*BackupService)
	bs.SetAuditLogger(logger)
	bs.SetAuditFailurePolicy(audit.FailOpen)

	info, err := svc.CreateBackup(context.Background(), BackupOptions{
		Destination: "/backups/set-after-construction",
		Encryption:  EncryptionNone,
	})
	if err != nil {
		t.Fatalf("CreateBackup after SetAuditFailurePolicy(FailOpen): %v", err)
	}
	if info.Status != BackupStatusComplete {
		t.Fatalf("status = %s, want complete", info.Status)
	}
}

// ---------------------------------------------------------------------------
// DecryptBackupShard's own typed errors (offline restore path)
// ---------------------------------------------------------------------------

// TestBackupService_DecryptBackupShard_ShardNotFoundTypedError proves a
// shard absent from the backup is reported as *BackupShardNotFoundError,
// not misreported as a decryption failure (*BackupDecryptionFailedError
// wrapping keywrap.UnwrapFailedError, as it was before the review's fix).
func TestBackupService_DecryptBackupShard_ShardNotFoundTypedError(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)
	bs := svc.(*BackupService)

	ctx := context.Background()
	created, err := bs.CreateBackup(ctx, BackupOptions{Destination: "/backups/shard-not-found", Encryption: EncryptionNone})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	_, err = bs.DecryptBackupShard(ctx, created.ID, 999, "")
	var notFound *BackupShardNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("err = %T, want *BackupShardNotFoundError", err)
	}
	var decFailed *BackupDecryptionFailedError
	if errors.As(err, &decFailed) {
		t.Fatalf("err must NOT also be a *BackupDecryptionFailedError (a missing shard is not a decryption failure): %v", err)
	}
}

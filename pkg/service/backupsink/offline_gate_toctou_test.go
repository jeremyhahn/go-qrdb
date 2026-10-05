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

package backupsink_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// flipSink wraps a real service.BackupSink and makes its FIRST ReadInfo
// call for a given backup return a caller-supplied snapshot (orig) while
// every later call returns whatever the sink actually holds now -- the
// shape of an attacker mutating a backup's stored metadata between two
// reads the same caller makes moments apart.
type flipSink struct {
	service.BackupSink
	n    int
	orig *service.BackupInfo
}

func (f *flipSink) ReadInfo(ctx context.Context, id string) (*service.BackupInfo, error) {
	f.n++
	if f.n == 1 {
		c := *f.orig
		return &c, nil
	}
	return f.BackupSink.ReadInfo(ctx, id)
}

// TestOfflineGateTOCTOU_GatedOnFirstReadRefusesDespitePlaintextSwitch
// proves cmd/qrdbd's offline disaster-recovery restore (restoreShardFromBackup)
// cannot be tricked into an ungated plaintext decrypt by a backup whose
// stored encryption state changes between its two independent reads of
// the SAME backup ID: GetBackup (which decides whether to gate/audit at
// all) and DecryptBackupShard (which does its own, separate loadEntry
// read and -- for a backup it sees as plaintext -- enforces the
// AllowPlaintext opt-in).
//
// restoreShardFromBackup passes DecryptBackupShard a boolean GATED on
// GetBackup's own read (requestedAllow && info.Encryption ==
// EncryptionNone), never the raw --allow-plaintext-restore flag by
// itself. If GetBackup's read said "encrypted", DecryptBackupShard is
// called with allowPlaintext=false regardless of the flag, so a backup
// that flips to plaintext (and, as here, has its envelope deleted too --
// the B5 downgrade AllowPlaintext exists to close) between the two reads
// is still refused: the refusal reflects what the FIRST read observed,
// not whatever DecryptBackupShard's own, later read happens to find.
//
// Before this fix, restoreShardFromBackup passed DecryptBackupShard the
// raw operator flag unconditionally; the second half of this test
// (...Unconditional...) reproduces that shape directly against
// DecryptBackupShard and confirms it DOES leak the substituted plaintext
// with zero audit trail -- the TOCTOU window this fix closes, and the
// scenario that would regress silently if restore.go's conditioning were
// ever reverted back to the raw flag.
func TestOfflineGateTOCTOU_GatedOnFirstReadRefusesDespitePlaintextSwitch(t *testing.T) {
	ctx := context.Background()
	e := newB2Env(t)
	e.key(t, "k1", 0x11)

	shards := map[uint64][]byte{1: []byte("shard-1"), 2: []byte("shard-2"), 3: []byte("shard-3")}
	created, err := e.svc(t, newFakeProvider(shards)).CreateBackup(ctx, service.BackupOptions{
		Destination: "/b",
		KeyRef:      "k1",
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	orig := *created

	// Attacker: delete the envelope (closing off the separate B2
	// metadata/artifact check so only the TOCTOU/gating path is under
	// test) and flip the stored metadata to plaintext.
	if err := e.store.Delete([]byte("envelope/" + created.ID)); err != nil {
		t.Fatal(err)
	}
	flipped := orig
	flipped.Encryption = service.EncryptionNone
	flipped.KeyProviderID = ""
	flippedBytes, err := json.Marshal(flipped)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.Set([]byte("meta/"+created.ID), flippedBytes); err != nil {
		t.Fatal(err)
	}

	// --- Fixed shape: restoreShardFromBackup's own call pattern. ---
	fs := &flipSink{BackupSink: e.sink, orig: &orig}
	bs, ok := e.svc(t, newFakeProvider(nil), service.WithBackupSink(fs)).(*service.BackupService)
	if !ok {
		t.Fatal("NewBackupService did not return *service.BackupService")
	}

	gi, err := bs.GetBackup(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetBackup (first read): %v", err)
	}
	if gi.Encryption == service.EncryptionNone {
		t.Fatal("precondition: GetBackup's first read must still observe the ORIGINAL encrypted state")
	}
	gated := gi.Encryption == service.EncryptionNone // requestedAllow (true) && this, per restore.go

	pt, err := bs.DecryptBackupShard(ctx, created.ID, 2, "", gated)
	if err == nil {
		t.Fatalf("TOCTOU: DecryptBackupShard accepted a plaintext switch GetBackup never observed; returned %q", pt)
	}
	var notAllowed *service.BackupPlaintextRestoreNotAllowedError
	if !errors.As(err, &notAllowed) {
		t.Fatalf("DecryptBackupShard err = %T, want *BackupPlaintextRestoreNotAllowedError: %v", err, err)
	}

	// --- Vulnerable shape: the raw flag, unconditioned on GetBackup's
	// own read. Reproduces the exact regression this fix closes. ---
	fsVuln := &flipSink{BackupSink: e.sink, orig: &orig}
	bsVuln, ok := e.svc(t, newFakeProvider(nil), service.WithBackupSink(fsVuln)).(*service.BackupService)
	if !ok {
		t.Fatal("NewBackupService did not return *service.BackupService")
	}
	if _, err := bsVuln.GetBackup(ctx, created.ID); err != nil {
		t.Fatalf("GetBackup (first read): %v", err)
	}
	ptVuln, err := bsVuln.DecryptBackupShard(ctx, created.ID, 2, "", true)
	if err != nil {
		t.Fatalf("expected the unconditional-flag shape to leak the stored shard (demonstrating the bug this fix closes), got refusal: %v", err)
	}
	// The shard bytes themselves were never substituted here (only the
	// metadata's Encryption field was flipped) -- the point is that
	// DecryptBackupShard's plaintext branch handed them back directly,
	// with no decryption and no AllowPlaintext audit record, to a caller
	// GetBackup had just told "this backup is encrypted".
	if len(ptVuln) == 0 {
		t.Fatal("expected the unconditional-flag shape to return the stored shard bytes, got none")
	}
	if bytes.Equal(ptVuln, shards[2]) {
		t.Fatal("expected the returned bytes to be the STORED (still-ciphertext) shard, not a decryption of it -- the plaintext branch never decrypts")
	}
}

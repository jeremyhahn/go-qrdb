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
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/service/backupsink"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
)

// b2Env bundles a durable sink and a key directory for the B2 downgrade
// tests below -- an attacker editing the sink's meta record directly,
// never going through the service that wrote it.
type b2Env struct {
	store  *objstore.Engine
	sink   *backupsink.Sink
	keyDir string
}

func newB2Env(t *testing.T) *b2Env {
	t.Helper()
	store, err := objstore.New("memory", map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &b2Env{store: store, sink: backupsink.New(store), keyDir: t.TempDir()}
}

func (e *b2Env) key(t *testing.T, name string, fill byte) {
	t.Helper()
	b := bytes.Repeat([]byte{fill}, 32)
	if err := os.WriteFile(filepath.Join(e.keyDir, name), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e *b2Env) svc(t *testing.T, p service.ShardSnapshotProvider, opts ...service.BackupServiceOption) service.BackupServicer {
	t.Helper()
	all := append([]service.BackupServiceOption{service.WithBackupSink(e.sink), service.WithKeyDir(e.keyDir)}, opts...)
	s, err := service.NewBackupService(p, all...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestBackupSink_B2DowngradeThroughFreshService proves the downgrade
// go-qrdb's security review found (#B2) is refused: an attacker who edits
// ONLY the durable sink's meta record (flips Encryption to "none",
// substitutes plaintext shard data, and recomputes the checksum to match
// it) while leaving the backup's ORIGINAL envelope object untouched in the
// sink cannot restore the substituted plaintext through a FRESH
// service.BackupService -- the shape cmd/qrdbd's offline, boot-time
// disaster-recovery restore (restoreShardFromBackup) actually
// constructs, which never shares the in-memory backupEntry the creating
// process held.
//
// Before the fix (pkg/service/backup_service.go's loadEntry read the
// envelope back only when info.Encryption != EncryptionNone), a fresh
// service never loaded the original envelope at all for a meta record
// that now claims "none": checkEncryptionConsistency saw a nil envelope
// and a "none" claim, read that as consistent, and the substituted
// plaintext restored (or decrypted, via DecryptBackupShard, the offline
// restore's own read path) cleanly.
func TestBackupSink_B2DowngradeThroughFreshService(t *testing.T) {
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
	if env, _ := e.sink.ReadEnvelope(ctx, created.ID); env == nil {
		t.Fatal("precondition: the original envelope must exist in the sink")
	}

	// Attacker: substitute plaintext shard data, flip Encryption to
	// "none", recompute the checksum over the substituted data. The
	// envelope object in the sink is left exactly as it was.
	evil := map[uint64][]byte{1: []byte("EVIL-1"), 2: []byte("EVIL-2"), 3: []byte("EVIL-3")}
	var dataBuf bytes.Buffer
	if err := gob.NewEncoder(&dataBuf).Encode(evil); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Set([]byte("data/"+created.ID), dataBuf.Bytes()); err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	for _, id := range []uint64{1, 2, 3} {
		h.Write(evil[id])
	}
	info := *created
	info.Encryption = service.EncryptionNone
	info.KeyProviderID = ""
	info.Checksum = hex.EncodeToString(h.Sum(nil))
	metaBytes, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.Set([]byte("meta/"+created.ID), metaBytes); err != nil {
		t.Fatal(err)
	}

	// A FRESH service -- the offline restore path never shares the
	// backupEntry the creating process held in memory.
	fp := newFakeProvider(map[uint64][]byte{})
	res, err := e.svc(t, fp).RestoreBackup(ctx, service.RestoreOptions{BackupID: created.ID, ValidateFirst: true})
	if err == nil {
		t.Fatalf("B2 downgrade accepted via RestoreBackup on a fresh service; imported %d shards: %v", len(fp.restored), fp.restored)
	}
	var mismatch *service.BackupMetadataMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("RestoreBackup err = %T, want *BackupMetadataMismatchError: %v", err, err)
	}
	if res != nil {
		t.Fatalf("expected a nil result alongside the refusal, got %+v", res)
	}
	if len(fp.restored) != 0 {
		t.Fatalf("a refused restore must import nothing, got %d shards", len(fp.restored))
	}

	// The identical check must hold on cmd/qrdbd's own read path
	// (DecryptBackupShard), which offline restore uses instead of
	// RestoreBackup.
	bs, ok := e.svc(t, newFakeProvider(nil)).(*service.BackupService)
	if !ok {
		t.Fatal("NewBackupService did not return *service.BackupService")
	}
	pt, err := bs.DecryptBackupShard(ctx, created.ID, 2, "")
	if err == nil {
		t.Fatalf("B2 downgrade accepted via DecryptBackupShard on a fresh service: returned %q", pt)
	}
	if !errors.As(err, &mismatch) {
		t.Fatalf("DecryptBackupShard err = %T, want *BackupMetadataMismatchError: %v", err, err)
	}
}

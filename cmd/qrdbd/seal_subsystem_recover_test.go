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

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// errRecoverFault is the injected backend failure of the boot-recovery tests.
var errRecoverFault = errors.New("injected boot-recovery backend fault")

// faultRecoverBackend wraps a seal-store backend and fails chosen calls: the
// next failGets reads, and, while failStagedDelete is set, every delete of a
// staged seal-store record (the last step of the roll-forward copy).
type faultRecoverBackend struct {
	seal.StorageBackend
	failGets         int
	failStagedDelete bool
}

func (f *faultRecoverBackend) Get(ctx context.Context, key string) ([]byte, error) {
	if f.failGets > 0 {
		f.failGets--
		return nil, errRecoverFault
	}
	return f.StorageBackend.Get(ctx, key)
}

func (f *faultRecoverBackend) Delete(ctx context.Context, key string) error {
	if f.failStagedDelete && strings.HasPrefix(key, sealStoreStagingPrefix) {
		return errRecoverFault
	}
	return f.StorageBackend.Delete(ctx, key)
}

// crashRecodeSealStorePastCommit runs a seal-store migration that crashes
// past its commit point, before the staged records are copied, and returns
// the seal-store directory (closed) and the secrets it holds.
func crashRecodeSealStorePastCommit(t *testing.T) (string, map[string]string) {
	t.Helper()
	ctx := context.Background()
	sealDir := filepath.Join(t.TempDir(), "seal")
	store, retirer, err := openSealStore(sealDir)
	if err != nil {
		t.Fatalf("openSealStore: %v", err)
	}
	backend := seal.NewStorageAdapter(store)
	source := newRecodeSourceBarrierOn(t, backend)
	secrets := map[string]string{"alpha": "alpha-recover-secret", "beta": "beta-recover-secret"}
	putRecodeSealStoreSecrets(t, source, secrets)
	walDir := filepath.Join(t.TempDir(), "wal")
	if err := os.MkdirAll(walDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	setRecodeSealStoreCommitFault(t, func() error { return errRecodeSealStoreCrash })
	err = recodeSystemBarrier(ctx, source, service.MigrationStores{}, walDir, validSystemMigrateOpts(), func() error { return retirer.Retire(ctx) })
	if !errors.Is(err, errRecodeSealStoreCrash) {
		t.Fatalf("err = %v, want %v", err, errRecodeSealStoreCrash)
	}
	_ = source.Close()
	_ = store.Close()
	return sealDir, secrets
}

// assertRecoverJournalPending checks the committed journal and its staged
// records are still there: the failed boot wrote nothing past them.
func assertRecoverJournalPending(t *testing.T, backend seal.StorageBackend) {
	t.Helper()
	ctx := context.Background()
	j, err := seal.LoadMigrationJournalFrom(ctx, backend, "system/root-key")
	if err != nil || j == nil || !j.Committed() || !journalNamesSealStore(j) {
		t.Fatalf("journal after the failed boot = %+v (err %v), want the committed seal-store journal kept", j, err)
	}
	staged, err := backend.List(ctx, sealStoreStagingPrefix)
	if err != nil || len(staged) == 0 {
		t.Fatalf("staged records after the failed boot = %v (err %v), want them kept", staged, err)
	}
}

// TestSealSubsystemRecoverInterrupted_JournalReadFailureStopsBoot: a failed
// first journal read is not "no pending migration". The boot returns the
// error before the recovery pass runs, so the recovery's own read cannot
// roll the seal store forward and clear the journal with no retirement
// marker written first.
func TestSealSubsystemRecoverInterrupted_JournalReadFailureStopsBoot(t *testing.T) {
	ctx := context.Background()
	sealDir, _ := crashRecodeSealStorePastCommit(t)
	store, retirer, err := openSealStore(sealDir)
	if err != nil {
		t.Fatalf("reopen the seal store: %v", err)
	}
	defer func() { _ = store.Close() }()
	backend := seal.NewStorageAdapter(store)
	faulty := &faultRecoverBackend{StorageBackend: backend, failGets: 1}
	if err := recoverSystemMigration(ctx, faulty, retirer, "system/root-key", nil); !errors.Is(err, errRecoverFault) {
		t.Fatalf("boot with a failing journal read: err = %v, want %v", err, errRecoverFault)
	}
	assertRecoverJournalPending(t, backend)
	if owed, merr := retirer.marked(); merr != nil || owed {
		t.Fatalf("retirement marker after the failed read = %v (err %v), want none written", owed, merr)
	}
}

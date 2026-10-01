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

package database

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony/ceremonytest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestShamirDatabase_UpdateBarrierAndScheme pins that a Shamir barrier's
// strategy and M-of-N change only through its holders' rekey: UpdateBarrier
// refuses both, and RecordShamirScheme records what a completed rekey left.
func TestShamirDatabase_UpdateBarrierAndScheme(t *testing.T) {
	ctx := context.Background()
	ca, _ := ceremonytest.NewCA(t, "Database Holder CA")
	dataDir := t.TempDir()
	bm := holderBarrierManager(t, ca)
	mgr := NewManager(nil, dataDir, WithBarrierManager(bm))
	cfg := shamirDBConfig(t, "vault")
	if _, err := mgr.CreateWithCredentials(ctx, cfg, seal.Credentials{}); err != nil {
		t.Fatal(err)
	}

	var migration *BarrierMigrationNotSupportedError
	toPassphrase := &DatabaseBarrierConfig{Enabled: true, Mode: BarrierModePassphrase}
	if err := mgr.UpdateBarrier("vault", toPassphrase); !errors.As(err, &migration) {
		t.Fatalf("switching to passphrase = %v, want BarrierMigrationNotSupportedError", err)
	}
	wider := &DatabaseBarrierConfig{Enabled: true, Mode: BarrierModeShamir, SharesRequired: 2, SharesTotal: 3}
	if err := mgr.UpdateBarrier("vault", wider); !errors.As(err, &migration) {
		t.Fatalf("changing the M-of-N = %v, want BarrierMigrationNotSupportedError", err)
	}
	same := &DatabaseBarrierConfig{Enabled: true, Mode: BarrierModeShamir, SharesRequired: 2, SharesTotal: 2}
	if err := mgr.UpdateBarrier("vault", same); err != nil {
		t.Fatalf("an unchanged Shamir barrier = %v, want nil", err)
	}

	if err := mgr.RecordShamirScheme("vault", 3, 4); err != nil {
		t.Fatal(err)
	}
	db, err := mgr.Get("vault")
	if err != nil {
		t.Fatal(err)
	}
	if got := db.Config().Barrier; got.SharesRequired != 3 || got.SharesTotal != 4 {
		t.Fatalf("recorded scheme = %d-of-%d, want 3-of-4", got.SharesRequired, got.SharesTotal)
	}
	if err := mgr.Close(); err != nil {
		t.Fatal(err)
	}
	mgr2 := NewManager(nil, dataDir)
	defer func() { _ = mgr2.Close() }()
	db2, err := mgr2.Open("vault")
	if err != nil {
		t.Fatal(err)
	}
	if got := db2.Config().Barrier; got.SharesRequired != 3 || got.SharesTotal != 4 {
		t.Fatalf("reopened scheme = %d-of-%d, want 3-of-4", got.SharesRequired, got.SharesTotal)
	}

	var notShamir *NotShamirBarrierError
	plain := NewManager(nil, t.TempDir())
	defer func() { _ = plain.Close() }()
	if _, err := plain.Create(validMemoryConfig("plain")); err != nil {
		t.Fatal(err)
	}
	if err := plain.RecordShamirScheme("plain", 2, 2); !errors.As(err, &notShamir) {
		t.Fatalf("recording on a non-Shamir database = %v, want NotShamirBarrierError", err)
	}
	var notFound *NotFoundError
	if err := plain.RecordShamirScheme("absent", 2, 2); !errors.As(err, &notFound) {
		t.Fatalf("recording on an absent database = %v, want NotFoundError", err)
	}
}

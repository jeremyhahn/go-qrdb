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
	mgr := newCatalogManager(nil, dataDir, WithBarrierManager(bm))
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

	db, err := mgr.Get("vault")
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.RecordShamirScheme(db, 3, 4); err != nil {
		t.Fatal(err)
	}
	if got := db.Config().Barrier; got.SharesRequired != 3 || got.SharesTotal != 4 {
		t.Fatalf("recorded scheme = %d-of-%d, want 3-of-4", got.SharesRequired, got.SharesTotal)
	}
	if err := mgr.Close(); err != nil {
		t.Fatal(err)
	}
	mgr2 := newCatalogManager(nil, dataDir)
	defer func() { _ = mgr2.Close() }()
	db2, err := mgr2.Open(context.Background(), "acme", "vault")
	if err != nil {
		t.Fatal(err)
	}
	if got := db2.Config().Barrier; got.SharesRequired != 3 || got.SharesTotal != 4 {
		t.Fatalf("reopened scheme = %d-of-%d, want 3-of-4", got.SharesRequired, got.SharesTotal)
	}

	var notShamir *NotShamirBarrierError
	plain := newCatalogManager(nil, t.TempDir())
	defer func() { _ = plain.Close() }()
	plainDB, err := plain.Create(validMemoryConfig("plain"))
	if err != nil {
		t.Fatal(err)
	}
	if err := plain.RecordShamirScheme(plainDB, 2, 2); !errors.As(err, &notShamir) {
		t.Fatalf("recording on a non-Shamir database = %v, want NotShamirBarrierError", err)
	}
}

// TestShamirDatabase_RecordShamirScheme_RecordsOnTheAuthorizedDatabase pins
// that a rekey's scheme lands on the database the caller authorized, not on
// whichever database a name lookup finds: two tenants each hold a Shamir
// database named vault, and recording on one leaves the other alone.
func TestShamirDatabase_RecordShamirScheme_RecordsOnTheAuthorizedDatabase(t *testing.T) {
	ctx := context.Background()
	ca, _ := ceremonytest.NewCA(t, "Database Holder CA")
	dataDir := t.TempDir()
	mgr := newCatalogManager(nil, dataDir, WithBarrierManager(holderBarrierManager(t, ca)))
	defer func() { _ = mgr.Close() }()
	cat := testCatalogFor(dataDir)
	acme, err := mgr.CreateWithCredentials(ctx, shamirDBConfig(t, "vault"), seal.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	globexCfg := shamirDBConfig(t, "vault")
	globexCfg.TenantID = "globex"
	globex, err := mgr.CreateWithCredentials(ctx, globexCfg, seal.Credentials{})
	if err != nil {
		t.Fatal(err)
	}

	if err := mgr.RecordShamirScheme(globex, 3, 4); err != nil {
		t.Fatalf("RecordShamirScheme on globex's vault: %v", err)
	}
	for _, c := range []struct {
		tenant string
		db     *Database
		m, n   int
	}{{"globex", globex, 3, 4}, {"acme", acme, 2, 2}} {
		if got := c.db.Config().Barrier; got.SharesRequired != c.m || got.SharesTotal != c.n {
			t.Fatalf("%s's open scheme = %d-of-%d, want %d-of-%d", c.tenant, got.SharesRequired, got.SharesTotal, c.m, c.n)
		}
		meta, err := cat.Get(ctx, c.tenant, "vault")
		if err != nil {
			t.Fatalf("catalog Get %s: %v", c.tenant, err)
		}
		if got := meta.Barrier; got.SharesRequired != c.m || got.SharesTotal != c.n {
			t.Fatalf("%s's recorded scheme = %d-of-%d, want %d-of-%d", c.tenant, got.SharesRequired, got.SharesTotal, c.m, c.n)
		}
	}
}

// TestShamirDatabase_RecordShamirScheme_RefusesADroppedDatabase pins that a
// rekey completing after its database was dropped does not write a record
// back for it.
func TestShamirDatabase_RecordShamirScheme_RefusesADroppedDatabase(t *testing.T) {
	ctx := context.Background()
	ca, _ := ceremonytest.NewCA(t, "Database Holder CA")
	dataDir := t.TempDir()
	mgr := newCatalogManager(nil, dataDir, WithBarrierManager(holderBarrierManager(t, ca)))
	defer func() { _ = mgr.Close() }()
	cat := testCatalogFor(dataDir)
	db, err := mgr.CreateWithCredentials(ctx, shamirDBConfig(t, "vault"), seal.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	if err := cat.Delete(ctx, "acme", "vault", db.Incarnation(), ""); err != nil {
		t.Fatalf("catalog Delete: %v", err)
	}

	var notFound *NotFoundError
	if err := mgr.RecordShamirScheme(db, 3, 4); !errors.As(err, &notFound) {
		t.Fatalf("recording on a dropped database = %v, want NotFoundError", err)
	}
	if _, err := cat.Get(ctx, "acme", "vault"); !errors.As(err, &notFound) {
		t.Fatalf("catalog Get after the refused record = %v, want NotFoundError", err)
	}
}

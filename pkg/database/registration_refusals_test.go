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

package database

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/logdb"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony/ceremonytest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestShamirDatabase_RegistrationFailures: registering a database's Shamir
// barrier a second time is refused and leaves the first registration
// serving; a holder authority that cannot be built (no ceremony CA) is
// refused and removes the barrier it had registered; and a holder authority
// whose barrier has gone from the registry is not handed out.
func TestShamirDatabase_RegistrationFailures(t *testing.T) {
	ctx := context.Background()
	ca, _ := ceremonytest.NewCA(t, "Database Holder CA")
	cfg := &DatabaseBarrierConfig{Enabled: true, Mode: BarrierModeShamir, SharesRequired: 2, SharesTotal: 2}

	bm := holderBarrierManager(t, ca)
	if err := bm.EnsureBarrier(ctx, "twice", "acme", cfg, seal.NewMemoryBackend(), seal.Credentials{}); err != nil {
		t.Fatal(err)
	}
	var create *BarrierCreateError
	if err := bm.EnsureBarrier(ctx, "twice", "acme", cfg, seal.NewMemoryBackend(), seal.Credentials{}); !errors.As(err, &create) || create.Name != "twice" {
		t.Fatalf("a second registration = %v, want *BarrierCreateError", err)
	}
	if _, _, err := bm.Holders("twice"); err != nil {
		t.Fatalf("the first registration after a refused second = %v", err)
	}

	// A ceremony CA is not needed to register: the holder authority serves
	// its verify-only operations without one (CA m829).
	noCA := newTestBarrierManager(t)
	noCA.SetHolderDeps(ceremony.HolderDeps{})
	if err := noCA.EnsureBarrier(ctx, "no-ca", "acme", cfg, seal.NewMemoryBackend(), seal.Credentials{}); err != nil {
		t.Fatalf("registration without a ceremony CA = %v, want success", err)
	}
	if _, _, err := noCA.Holders("no-ca"); err != nil {
		t.Fatalf("the holder authority of a database registered without a CA = %v", err)
	}

	refused := newTestBarrierManager(t)
	refused.SetHolderDeps(ceremony.HolderDeps{})
	refused.logger = nil
	if err := refused.EnsureBarrier(ctx, "refused", "acme", cfg, seal.NewMemoryBackend(), seal.Credentials{}); !errors.As(err, &create) || !errors.Is(err, ceremony.ErrNilLogger) {
		t.Fatalf("registration whose holder authority is refused = %v, want *BarrierCreateError wrapping ErrNilLogger", err)
	}
	if _, err := refused.registry.GetTenant("refused"); err == nil {
		t.Fatal("a refused registration left its barrier in the registry")
	}

	if err := bm.registry.RemoveTenant("twice"); err != nil {
		t.Fatal(err)
	}
	var notShamir *NotShamirBarrierError
	if _, _, err := bm.Holders("twice"); !errors.As(err, &notShamir) {
		t.Fatalf("Holders with the barrier gone = %v, want *NotShamirBarrierError", err)
	}
}

// TestBarrierMode_DefaultsToPassphrase: no barrier config, or one with no
// mode, is the passphrase strategy.
func TestBarrierMode_DefaultsToPassphrase(t *testing.T) {
	for _, cfg := range []*DatabaseBarrierConfig{nil, {Enabled: true}} {
		if got := barrierMode(cfg); got != BarrierModePassphrase {
			t.Fatalf("barrierMode(%+v) = %q, want passphrase", cfg, got)
		}
	}
	if got := barrierMode(&DatabaseBarrierConfig{Mode: BarrierModeShamir}); got != BarrierModeShamir {
		t.Fatalf("barrierMode(shamir) = %q", got)
	}
}

// protectedLogDB is a shared WAL that declares its at-rest protection.
type protectedLogDB struct {
	*fakeLogDB
	protection logdb.AtRestProtection
}

func (p protectedLogDB) AtRestProtection() logdb.AtRestProtection { return p.protection }

// TestMultiplexLogDB_AtRestProtectionIsTheSharedWALs: the multiplex declares
// what its shared WAL declares, and a shared WAL that declares nothing is
// unprotected -- fail closed.
func TestMultiplexLogDB_AtRestProtectionIsTheSharedWALs(t *testing.T) {
	plain, err := NewMultiplexLogDB(&fakeLogDB{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := plain.AtRestProtection(); got != logdb.AtRestUnprotected {
		t.Fatalf("undeclared shared WAL = %v, want unprotected", got)
	}
	enc, err := NewMultiplexLogDB(protectedLogDB{fakeLogDB: &fakeLogDB{}, protection: logdb.AtRestBarrierEncrypted}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := enc.AtRestProtection(); got != logdb.AtRestBarrierEncrypted {
		t.Fatalf("encrypted shared WAL = %v, want barrier-encrypted", got)
	}
}

// TestDatabaseErrors_MessagesAndUnwrap covers the messages and unwrap chains
// of the incarnation and Raft WAL errors.
func TestDatabaseErrors_MessagesAndUnwrap(t *testing.T) {
	cause := errors.New("cause")
	inc := &IncarnationError{Err: cause}
	if !errors.Is(inc, cause) || !strings.Contains(inc.Error(), "incarnation") {
		t.Fatalf("IncarnationError = %q", inc.Error())
	}
	marker := &RaftWALMarkerError{Path: "/wal/FORMAT", Err: cause}
	if !errors.Is(marker, cause) || !strings.Contains(marker.Error(), "/wal/FORMAT") {
		t.Fatalf("RaftWALMarkerError = %q", marker.Error())
	}
	known := &RaftWALFormatError{Dir: "/wal", Found: WALFormat("plaintext"), Want: WALFormat("barrier")}
	if msg := known.Error(); !strings.Contains(msg, "written as plaintext") || !strings.Contains(msg, "writes barrier") {
		t.Fatalf("RaftWALFormatError = %q", msg)
	}
	unmarked := &RaftWALFormatError{Dir: "/wal", Want: WALFormat("barrier")}
	if msg := unmarked.Error(); !strings.Contains(msg, "unknown (unmarked)") {
		t.Fatalf("unmarked RaftWALFormatError = %q", msg)
	}
}

// TestSetBarrierManager_ARegistrationFailureSkipsOnlyThatDatabase: a
// replacement barrier manager that cannot register a Shamir database (it has
// no share-holder custody wired) leaves that database unregistered, logs it,
// and does not abort the call.
func TestSetBarrierManager_ARegistrationFailureSkipsOnlyThatDatabase(t *testing.T) {
	ca, _ := ceremonytest.NewCA(t, "Database Holder CA")
	mgr := NewManager(nil, t.TempDir(), WithBarrierManager(holderBarrierManager(t, ca)))
	defer func() { _ = mgr.Close() }()
	if _, err := mgr.CreateWithCredentials(context.Background(), shamirDBConfig(t, "vault"), seal.Credentials{}); err != nil {
		t.Fatal(err)
	}

	noCustody := newTestBarrierManager(t)
	mgr.SetBarrierManager(noCustody)
	if noCustody.GetBarrier("vault") != nil {
		t.Fatal("a manager with no holder custody registered a Shamir database")
	}
	if _, _, err := noCustody.Holders("vault"); err == nil {
		t.Fatal("a manager with no holder custody has a holder authority for the database")
	}
}

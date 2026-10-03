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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// A create's barrier is refused as the caller's error, not a server fault, on
// every transport that classifies through errclass, and the refused create
// leaves no database behind:
//
//   - a strategy per-database barriers do not implement is InvalidArgument
//     (*database.UnsupportedBarrierStrategyError);
//   - a Shamir barrier on a node whose barrier manager has no share-holder
//     custody wired is FailedPrecondition
//     (*database.ShamirCustodyNotConfiguredError). Per-database Shamir is
//     served where custody is wired (docs/databases); this is what the
//     database wizard, and any other client, meets on a node without it.
func TestDatabaseBinding_CreateWithAnUnservedBarrierIsRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		barrier *transport.DatabaseBarrierConfig
		want    errclass.Class
		check   func(error) bool
	}{
		"unknown strategy": {
			barrier: &transport.DatabaseBarrierConfig{Enabled: true, Mode: "tpm"},
			want:    errclass.InvalidArgument,
			check: func(err error) bool {
				var unsupported *database.UnsupportedBarrierStrategyError
				return errors.As(err, &unsupported) && unsupported.Mode == "tpm"
			},
		},
		"shamir without custody": {
			barrier: &transport.DatabaseBarrierConfig{Enabled: true, Mode: "shamir", SharesRequired: 2, SharesTotal: 3},
			want:    errclass.FailedPrecondition,
			check: func(err error) bool {
				var custody *database.ShamirCustodyNotConfiguredError
				return errors.As(err, &custody)
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc := barrierDatabaseService(t)
			b := NewDatabaseBinding(testLogger(t), newTestConnectionBinding(t, newMockKVService(), newMockAdminService(),
				embedded.WithDatabaseService(svc)))
			_, err := b.Create(&transport.CreateDatabaseRequest{
				Name:    "refused-db",
				Engine:  "kv",
				Backend: "pebble",
				DataDir: t.TempDir(),
				Barrier: tc.barrier,
			})
			if !tc.check(err) {
				t.Fatalf("create = %v (%T)", err, err)
			}
			if got := errclass.Classify(err); got != tc.want {
				t.Fatalf("the refusal classifies as %v, want %v", got, tc.want)
			}
			if _, getErr := svc.Get("refused-db"); getErr == nil {
				t.Fatal("the refused create left the database behind")
			}
		})
	}
}

// TestBindings_WizardShamirSequence drives, through the desktop bindings the
// database wizard calls in the desktop app, the wizard's Shamir sequence on a
// server whose database barrier manager has share-holder custody wired
// (docs/desktop-gui/engineering.md): the create carries the M-of-N and no
// secret and leaves the database sealed, exactly N holders are enrolled
// through the database scope, and initialization splits the key at the
// create's M-of-N. Initialization refuses before all N are enrolled. No
// claim or submission is part of it.
func TestBindings_WizardShamirSequence(t *testing.T) {
	fx := servicetest.NewShamirDatabase(t, "existing", 2, 2)
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService(),
		embedded.WithDatabaseService(fx.Service))
	dbs := NewDatabaseBinding(testLogger(t), conn)
	officers := NewOfficerBinding(testLogger(t), conn)

	info, err := dbs.Create(&transport.CreateDatabaseRequest{
		Name:    "wizard-vault",
		Engine:  "kv",
		Backend: "memory",
		DataDir: t.TempDir(),
		Barrier: &transport.DatabaseBarrierConfig{Enabled: true, Mode: "shamir", SharesRequired: 2, SharesTotal: 3},
	})
	if err != nil {
		t.Fatalf("Create (shamir 2-of-3) = %v", err)
	}
	if info.Status != string(database.StatusSealed) {
		t.Fatalf("a Shamir database before its custody is initialized has status %q, want sealed", info.Status)
	}

	scope := HolderScope{Database: "wizard-vault"}
	for i, name := range []string{"alice", "bob", "carol"} {
		if i == 2 {
			if _, err := officers.InitializeDatabaseShamir("wizard-vault", ""); err == nil {
				t.Fatal("initialization with 2 of 3 holders enrolled was admitted")
			}
		}
		h := servicetest.NewHolder(t, name)
		if _, err := officers.RegisterHolder(scope, &transport.OfficerRegisterRequest{
			Username: name, Role: transport.RoleCustodian, CSRPEM: h.CSRPEM,
		}); err != nil {
			t.Fatalf("RegisterHolder(%s): %v", name, err)
		}
	}
	// A fourth holder -- a second tab, the database view, the CLI -- is
	// refused: initialization needs exactly N, and before it no holder can
	// be revoked.
	extra := servicetest.NewHolder(t, "dave")
	_, err = officers.RegisterHolder(scope, &transport.OfficerRegisterRequest{Username: "dave", Role: transport.RoleCustodian, CSRPEM: extra.CSRPEM})
	var limit *ceremony.HolderLimitReachedError
	if !errors.As(err, &limit) || limit.Total != 3 {
		t.Fatalf("RegisterHolder past N = %v (%T), want HolderLimitReachedError{Total: 3}", err, err)
	}
	if got := errclass.Classify(err); got != errclass.FailedPrecondition {
		t.Fatalf("the refusal classifies as %v, want FailedPrecondition", got)
	}

	before, err := dbs.BarrierStatus("wizard-vault")
	if err != nil {
		t.Fatalf("BarrierStatus before initialization = %v", err)
	}
	if !before.InitializedAt.IsZero() || !before.Sealed {
		t.Fatalf("status before initialization = %+v, want sealed and never initialized", before)
	}
	split, err := officers.InitializeDatabaseShamir("wizard-vault", "")
	if err != nil {
		t.Fatalf("InitializeDatabaseShamir = %v", err)
	}
	if split.Threshold != 2 || split.TotalShares != 3 || split.ShareCount != 3 {
		t.Fatalf("split = %+v, want 2-of-3 with 3 envelopes", split)
	}
	listed, err := officers.ListHolders(scope)
	if err != nil || len(listed) != 3 {
		t.Fatalf("ListHolders = %d holders, %v", len(listed), err)
	}
	for _, h := range listed {
		if h.ShareIndex == 0 || !h.SharePending {
			t.Fatalf("holder %s after initialization: share %d, pending %v; want a share awaiting its claim", h.Username, h.ShareIndex, h.SharePending)
		}
	}
	after, err := dbs.BarrierStatus("wizard-vault")
	if err != nil {
		t.Fatalf("BarrierStatus after initialization = %v", err)
	}
	if after.InitializedAt.IsZero() || after.Sealed || after.Strategy != "shamir" {
		t.Fatalf("status after initialization = %+v, want an initialized, unsealed shamir barrier", after)
	}
	if _, err := officers.InitializeDatabaseShamir("wizard-vault", ""); err == nil {
		t.Fatal("a second initialization was admitted")
	}
}

// BarrierStatus reports the binding's connection failure and the service's
// refusal unchanged.
func TestDatabaseBinding_BarrierStatusRefusals(t *testing.T) {
	notConnected := NewDatabaseBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	if _, err := notConnected.BarrierStatus("vault"); err == nil {
		t.Fatal("BarrierStatus without a connection succeeded")
	}
	fx := servicetest.NewShamirDatabase(t, "vault", 2, 2)
	b := NewDatabaseBinding(testLogger(t), newTestConnectionBinding(t, newMockKVService(), newMockAdminService(),
		embedded.WithDatabaseService(fx.Service)))
	if _, err := b.BarrierStatus("missing"); err == nil {
		t.Fatal("BarrierStatus of a missing database succeeded")
	}
}

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
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func validSystemMigrateOpts() seal.MigrationOptions {
	return seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials([]byte("daemon-migrate-pw")),
	}
}

// TestDaemonSystemMigrator_EnqueuesAndReportsOutcome proves a ScopeSystem
// request is handed to the boot loop over migrateCh and the caller blocks until
// the loop reports the recycle outcome.
func TestDaemonSystemMigrator_EnqueuesAndReportsOutcome(t *testing.T) {
	ch := make(chan *systemMigrationRequest, 1)
	m := newDaemonSystemMigrator(ch)

	// Stand-in boot loop: receive the request and report success.
	go func() {
		req := <-ch
		if req.opts.TargetStrategy != seal.StrategyPassphrase {
			t.Errorf("boot loop got strategy %q", req.opts.TargetStrategy)
		}
		req.done <- nil
	}()

	err := m.EnableEncryption(context.Background(),
		service.BarrierScope{Kind: service.ScopeSystem}, validSystemMigrateOpts())
	if err != nil {
		t.Fatalf("EnableEncryption: %v", err)
	}
}

// TestDaemonSystemMigrator_PropagatesRecodeError proves a recycle failure
// reported by the boot loop propagates back to the caller.
func TestDaemonSystemMigrator_PropagatesRecodeError(t *testing.T) {
	ch := make(chan *systemMigrationRequest, 1)
	m := newDaemonSystemMigrator(ch)
	sentinel := errors.New("recode boom")

	go func() {
		req := <-ch
		req.done <- sentinel
	}()

	err := m.EnableEncryption(context.Background(),
		service.BarrierScope{Kind: service.ScopeSystem}, validSystemMigrateOpts())
	if !errors.Is(err, sentinel) {
		t.Fatalf("EnableEncryption error = %v, want %v", err, sentinel)
	}
}

// TestDaemonSystemMigrator_InProgressRejected proves a second system migration
// while one is pending (channel full, boot loop busy) is rejected rather than
// queued.
func TestDaemonSystemMigrator_InProgressRejected(t *testing.T) {
	ch := make(chan *systemMigrationRequest, 1)
	m := newDaemonSystemMigrator(ch)

	// Occupy the single-slot channel with no consumer: the boot loop is "busy".
	ch <- &systemMigrationRequest{opts: validSystemMigrateOpts(), done: make(chan error, 1)}

	err := m.EnableEncryption(context.Background(),
		service.BarrierScope{Kind: service.ScopeSystem}, validSystemMigrateOpts())
	if !errors.Is(err, ErrSystemMigrationInProgress) {
		t.Fatalf("EnableEncryption error = %v, want ErrSystemMigrationInProgress", err)
	}
}

// TestDaemonSystemMigrator_NonSystemScopeRejected proves the daemon system
// migrator rejects a non-system scope (the scoped composite must route tenant
// scope elsewhere, never here).
func TestDaemonSystemMigrator_NonSystemScopeRejected(t *testing.T) {
	m := newDaemonSystemMigrator(make(chan *systemMigrationRequest, 1))
	err := m.EnableEncryption(context.Background(),
		service.BarrierScope{Kind: service.ScopeTenant, TenantID: "acme"}, validSystemMigrateOpts())
	if !errors.Is(err, service.ErrMigrationProviderUnavailable) {
		t.Fatalf("tenant scope error = %v, want ErrMigrationProviderUnavailable", err)
	}
}

// TestDaemonSystemMigrator_InvalidOptsRejectedBeforeEnqueue proves option
// validation runs before the request reaches the boot loop (nothing is enqueued
// for invalid input).
func TestDaemonSystemMigrator_InvalidOptsRejectedBeforeEnqueue(t *testing.T) {
	ch := make(chan *systemMigrationRequest, 1)
	m := newDaemonSystemMigrator(ch)

	// StrategyNone is not a valid migration target.
	bad := validSystemMigrateOpts()
	bad.TargetStrategy = seal.StrategyNone
	err := m.EnableEncryption(context.Background(),
		service.BarrierScope{Kind: service.ScopeSystem}, bad)
	if err == nil {
		t.Fatal("invalid opts must be rejected")
	}
	select {
	case <-ch:
		t.Fatal("invalid request must not be enqueued")
	case <-time.After(50 * time.Millisecond):
	}
}

// TestKeyedSystemStrategy covers the keyed-strategy selection used by the
// system recode. Only a PASSPHRASE target yields a self-contained sealing
// strategy; a Shamir target is minted through recodeSystemBarrier's
// MintKeyedShamirSibling branch (a SealingStrategy cannot represent Shamir's
// ceremony), so keyedSystemStrategy rejects both Shamir and None.
func TestKeyedSystemStrategy(t *testing.T) {
	s, err := keyedSystemStrategy(seal.MigrationOptions{TargetStrategy: seal.StrategyPassphrase})
	if err != nil || s == nil {
		t.Fatalf("keyedSystemStrategy(passphrase) = %v, %v", s, err)
	}
	for _, id := range []seal.StrategyID{seal.StrategyShamir, seal.StrategyNone} {
		if _, err := keyedSystemStrategy(seal.MigrationOptions{TargetStrategy: id}); err == nil {
			t.Fatalf("keyedSystemStrategy(%q) must error (not a self-contained keyed strategy)", id)
		}
	}
}

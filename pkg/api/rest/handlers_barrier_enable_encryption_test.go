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

package rest

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// captureMigrator records the EnableEncryption call for assertions.
type captureMigrator struct {
	ch  chan migrateCall
	err error
}

type migrateCall struct {
	scope service.BarrierScope
	opts  seal.MigrationOptions
}

// EnableEncryption records the call. opts.Credentials.Secret is a BORROW
// valid only for the duration of this call — the handler owns that slice and
// zeroizes it once the migration returns — so the recorded copy must be
// deep. Recording the slice header instead would hand the assertions a
// buffer that is legitimately zeroed out from under them.
func (m *captureMigrator) EnableEncryption(_ context.Context, scope service.BarrierScope, opts seal.MigrationOptions) error {
	opts.Credentials.Secret = bytes.Clone(opts.Credentials.Secret)
	m.ch <- migrateCall{scope: scope, opts: opts}
	return m.err
}

func newEnableEncTestServer(t *testing.T, mig service.EncryptionMigrator) *Server {
	t.Helper()
	opts := []ServerOption{WithBarrierService(newExtBarrierSvc())}
	if mig != nil {
		opts = append(opts, WithEncryptionMigrator(mig))
	}
	return NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", opts...)
}

func TestHandleBarrierEnableEncryption_NotWired(t *testing.T) {
	// No migrator configured → the route is not even registered, so a request
	// is a 404/405; assert it is not a success and not a 202-style start.
	srv := newEnableEncTestServer(t, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/enable-encryption",
		strings.NewReader(`{"strategy":"passphrase","passphrase":"cHc="}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("expected non-200 when no migrator is wired, got %d", rec.Code)
	}
}

func TestHandleBarrierEnableEncryption_InvalidOptionsRejectedSynchronously(t *testing.T) {
	mig := &captureMigrator{ch: make(chan migrateCall, 1)}
	srv := newEnableEncTestServer(t, mig)
	// strategy "none" is an invalid migration target → 400 before any async work.
	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/enable-encryption",
		strings.NewReader(`{"strategy":"none"}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid options: status = %d, want 400", rec.Code)
	}
	select {
	case <-mig.ch:
		t.Fatal("migrator must not be invoked for invalid options")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestHandleBarrierEnableEncryption_SystemStarts(t *testing.T) {
	mig := &captureMigrator{ch: make(chan migrateCall, 1)}
	srv := newEnableEncTestServer(t, mig)
	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/enable-encryption",
		strings.NewReader(`{"strategy":"passphrase","mechanism":"swap","passphrase":"cHc="}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp map[string]interface{}
	decodeJSON(t, rec, &resp)
	if resp["status"] != "migration_started" {
		t.Fatalf("status = %v, want migration_started", resp["status"])
	}
	if resp["scope"] != "system" {
		t.Fatalf("scope = %v, want system", resp["scope"])
	}

	// The migration runs asynchronously after a flush grace; wait for it.
	select {
	case call := <-mig.ch:
		if call.scope.Kind != service.ScopeSystem {
			t.Fatalf("scope kind = %q, want system", call.scope.Kind)
		}
		if call.opts.TargetStrategy != seal.StrategyPassphrase {
			t.Fatalf("target = %q, want passphrase", call.opts.TargetStrategy)
		}
		if call.opts.Mechanism != seal.MechanismSwap {
			t.Fatalf("mechanism = %q, want swap", call.opts.Mechanism)
		}
		if string(call.opts.Credentials.Secret) != "pw" {
			t.Fatalf("passphrase not threaded to credentials")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("migrator was not invoked within 3s")
	}
}

// TestHandleBarrierEnableEncryption_AcceptanceFailureSurfacesSynchronously
// proves that a synchronously-knowable rejection from the migrator (e.g.
// shared-barrier-immutable, entitlement denied, insufficient officers) is
// returned to the caller as a proper error instead of the client seeing a
// false "migration_started" while nothing actually happened (Codex #6).
func TestHandleBarrierEnableEncryption_AcceptanceFailureSurfacesSynchronously(t *testing.T) {
	mig := &captureMigrator{ch: make(chan migrateCall, 1), err: service.ErrSharedBarrierImmutable}
	srv := newEnableEncTestServer(t, mig)
	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/enable-encryption",
		strings.NewReader(`{"tenant_id":"acme","strategy":"passphrase","passphrase":"cHc="}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (ErrSharedBarrierImmutable)", rec.Code)
	}
	var resp map[string]interface{}
	decodeJSON(t, rec, &resp)
	if resp["status"] == "migration_started" {
		t.Fatal("must not report migration_started when acceptance failed synchronously")
	}

	select {
	case <-mig.ch:
	case <-time.After(3 * time.Second):
		t.Fatal("migrator was not invoked")
	}
}

func TestHandleBarrierEnableEncryption_TenantScope(t *testing.T) {
	mig := &captureMigrator{ch: make(chan migrateCall, 1)}
	srv := newEnableEncTestServer(t, mig)
	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/enable-encryption",
		strings.NewReader(`{"tenant_id":"acme","strategy":"passphrase","passphrase":"cHc="}`))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp map[string]interface{}
	decodeJSON(t, rec, &resp)
	if resp["scope"] != "tenant" {
		t.Fatalf("scope = %v, want tenant", resp["scope"])
	}
	select {
	case call := <-mig.ch:
		if call.scope.Kind != service.ScopeTenant || call.scope.TenantID != "acme" {
			t.Fatalf("scope = %+v, want tenant/acme", call.scope)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("migrator was not invoked")
	}
}

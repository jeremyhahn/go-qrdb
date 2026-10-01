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

package quic

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// captureQUICMigrator records the EnableEncryption call for assertions.
type captureQUICMigrator struct {
	ch  chan quicMigrateCall
	err error
}

type quicMigrateCall struct {
	ctx    context.Context // the context the migration runs on
	scope  service.BarrierScope
	opts   seal.MigrationOptions
	secret []byte // snapshot taken DURING the call; the handler destroys the credential after
}

func (m *captureQUICMigrator) EnableEncryption(ctx context.Context, scope service.BarrierScope, opts seal.MigrationOptions) error {
	// The credential is a borrow valid for this call only: the handler's
	// goroutine destroys it when this returns, so an early destroy would
	// surface here as a panic from Snapshot rather than pass unnoticed.
	m.ch <- quicMigrateCall{ctx: ctx, scope: scope, opts: opts, secret: sealtest.Snapshot(opts.Credentials)}
	return m.err
}

func TestHandleBarrierEnableEncryption_NilService(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	payload, _ := proto.Marshal(&pb.BarrierEnableEncryptionRequest{Strategy: "passphrase", Passphrase: []byte("pw")})

	_, err := srv.handleBarrierEnableEncryption(context.Background(), payload)
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestHandleBarrierEnableEncryption_InvalidOptionsRejectedSynchronously(t *testing.T) {
	mig := &captureQUICMigrator{ch: make(chan quicMigrateCall, 1)}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithEncryptionMigrator(mig))

	// strategy "none" is an invalid migration target.
	payload, _ := proto.Marshal(&pb.BarrierEnableEncryptionRequest{Strategy: "none"})
	_, err := srv.handleBarrierEnableEncryption(context.Background(), payload)
	if err == nil {
		t.Fatal("expected validation error for strategy=none")
	}
	select {
	case <-mig.ch:
		t.Fatal("migrator must not be invoked for invalid options")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestHandleBarrierEnableEncryption_SystemStarts(t *testing.T) {
	mig := &captureQUICMigrator{ch: make(chan quicMigrateCall, 1)}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithEncryptionMigrator(mig))

	payload, _ := proto.Marshal(&pb.BarrierEnableEncryptionRequest{
		Strategy:   "passphrase",
		Mechanism:  "swap",
		Passphrase: []byte("pw"),
	})

	respBytes, err := srv.handleBarrierEnableEncryption(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var resp pb.BarrierEnableEncryptionResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.GetStatus() != "migration_started" {
		t.Fatalf("status = %q, want migration_started", resp.GetStatus())
	}
	if resp.GetScope() != "system" {
		t.Fatalf("scope = %q, want system", resp.GetScope())
	}

	select {
	case call := <-mig.ch:
		if call.scope.Kind != service.ScopeSystem {
			t.Fatalf("scope.Kind = %v, want ScopeSystem", call.scope.Kind)
		}
		if string(call.secret) != "pw" {
			t.Fatalf("passphrase not threaded to credentials: %q", call.secret)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("migrator was never invoked")
	}
}

// TestHandleBarrierEnableEncryption_AcceptanceFailureSurfacesSynchronously
// proves that a synchronously-knowable rejection from the migrator (e.g.
// shared-barrier-immutable) is returned to the caller as a real error
// instead of the client seeing a false "migration_started" while nothing
// actually happened (Codex #6).
func TestHandleBarrierEnableEncryption_AcceptanceFailureSurfacesSynchronously(t *testing.T) {
	mig := &captureQUICMigrator{ch: make(chan quicMigrateCall, 1), err: service.ErrSharedBarrierImmutable}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithEncryptionMigrator(mig))

	payload, _ := proto.Marshal(&pb.BarrierEnableEncryptionRequest{
		TenantId: "acme", Strategy: "passphrase", Passphrase: []byte("pw"),
	})
	respBytes, err := srv.handleBarrierEnableEncryption(context.Background(), payload)
	if !errors.Is(err, service.ErrSharedBarrierImmutable) {
		t.Fatalf("expected ErrSharedBarrierImmutable, got %v", err)
	}
	if respBytes != nil {
		t.Fatalf("expected nil response on synchronous rejection, got %v", respBytes)
	}

	select {
	case <-mig.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("migrator was never invoked")
	}
}

func TestHandleBarrierEnableEncryption_TenantScope(t *testing.T) {
	mig := &captureQUICMigrator{ch: make(chan quicMigrateCall, 1)}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithEncryptionMigrator(mig))

	payload, _ := proto.Marshal(&pb.BarrierEnableEncryptionRequest{
		TenantId:        "tenant-a",
		Strategy:        "shamir",
		ShamirThreshold: 2,
		ShamirTotal:     3,
	})

	respBytes, err := srv.handleBarrierEnableEncryption(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var resp pb.BarrierEnableEncryptionResponse
	if err := proto.Unmarshal(respBytes, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.GetScope() != "tenant" {
		t.Fatalf("scope = %q, want tenant", resp.GetScope())
	}

	select {
	case call := <-mig.ch:
		if call.scope.Kind != service.ScopeTenant || call.scope.TenantID != "tenant-a" {
			t.Fatalf("unexpected scope: %+v", call.scope)
		}
		if call.opts.ShamirThreshold != 2 || call.opts.ShamirTotal != 3 {
			t.Fatalf("unexpected shamir params: %+v", call.opts)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("migrator was never invoked")
	}
}

func TestHandleBarrierEnableEncryption_DispatchRegistered(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithEncryptionMigrator(&captureQUICMigrator{ch: make(chan quicMigrateCall, 1)}))
	if _, ok := srv.handlers["barrier.EnableEncryption"]; !ok {
		t.Fatal("expected barrier.EnableEncryption to be registered in the dispatch table")
	}
}

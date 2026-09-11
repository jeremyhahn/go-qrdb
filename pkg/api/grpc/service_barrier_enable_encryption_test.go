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

package grpc

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// captureGRPCMigrator records the EnableEncryption call for assertions.
type captureGRPCMigrator struct {
	ch  chan grpcMigrateCall
	err error
}

type grpcMigrateCall struct {
	scope service.BarrierScope
	opts  seal.MigrationOptions
}

func (m *captureGRPCMigrator) EnableEncryption(_ context.Context, scope service.BarrierScope, opts seal.MigrationOptions) error {
	m.ch <- grpcMigrateCall{scope: scope, opts: opts}
	return m.err
}

func TestBarrierEnableEncryption_NotConfigured(t *testing.T) {
	client, cleanup := startSQLTestServer(t)
	defer cleanup()

	_, err := client.BarrierEnableEncryption(context.Background(), &pb.BarrierEnableEncryptionRequest{
		Strategy: "passphrase", Passphrase: []byte("pw"),
	})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("expected codes.Unimplemented, got %v", status.Code(err))
	}
}

func TestBarrierEnableEncryption_InvalidOptionsRejectedSynchronously(t *testing.T) {
	mig := &captureGRPCMigrator{ch: make(chan grpcMigrateCall, 1)}
	client, cleanup := startSQLTestServer(t, WithEncryptionMigrator(mig))
	defer cleanup()

	// strategy "none" is an invalid migration target -> InvalidArgument before
	// any async work.
	_, err := client.BarrierEnableEncryption(context.Background(), &pb.BarrierEnableEncryptionRequest{
		Strategy: "none",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected codes.InvalidArgument, got %v", status.Code(err))
	}
	select {
	case <-mig.ch:
		t.Fatal("migrator must not be invoked for invalid options")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestBarrierEnableEncryption_SystemStarts(t *testing.T) {
	mig := &captureGRPCMigrator{ch: make(chan grpcMigrateCall, 1)}
	client, cleanup := startSQLTestServer(t, WithEncryptionMigrator(mig))
	defer cleanup()

	resp, err := client.BarrierEnableEncryption(context.Background(), &pb.BarrierEnableEncryptionRequest{
		Strategy:   "passphrase",
		Mechanism:  "swap",
		Passphrase: []byte("pw"),
	})
	if err != nil {
		t.Fatalf("BarrierEnableEncryption failed: %v", err)
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
		if call.opts.TargetStrategy != "passphrase" {
			t.Fatalf("TargetStrategy = %v, want passphrase", call.opts.TargetStrategy)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("migrator was never invoked")
	}
}

// TestBarrierEnableEncryption_AcceptanceFailureSurfacesSynchronously proves
// that a synchronously-knowable rejection from the migrator (e.g.
// shared-barrier-immutable) is returned to the caller as a proper gRPC
// status error instead of the client seeing a false "migration_started"
// while nothing actually happened (Codex #6).
func TestBarrierEnableEncryption_AcceptanceFailureSurfacesSynchronously(t *testing.T) {
	mig := &captureGRPCMigrator{ch: make(chan grpcMigrateCall, 1), err: service.ErrSharedBarrierImmutable}
	client, cleanup := startSQLTestServer(t, WithEncryptionMigrator(mig))
	defer cleanup()

	resp, err := client.BarrierEnableEncryption(context.Background(), &pb.BarrierEnableEncryptionRequest{
		TenantId: "acme", Strategy: "passphrase", Passphrase: []byte("pw"),
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected codes.PermissionDenied, got %v (err=%v)", status.Code(err), err)
	}
	if resp != nil {
		t.Fatalf("expected nil response on synchronous rejection, got %+v", resp)
	}

	select {
	case <-mig.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("migrator was never invoked")
	}
}

func TestBarrierEnableEncryption_TenantScope(t *testing.T) {
	mig := &captureGRPCMigrator{ch: make(chan grpcMigrateCall, 1)}
	client, cleanup := startSQLTestServer(t, WithEncryptionMigrator(mig))
	defer cleanup()

	resp, err := client.BarrierEnableEncryption(context.Background(), &pb.BarrierEnableEncryptionRequest{
		TenantId:        "tenant-a",
		Strategy:        "shamir",
		ShamirThreshold: 2,
		ShamirTotal:     3,
	})
	if err != nil {
		t.Fatalf("BarrierEnableEncryption failed: %v", err)
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

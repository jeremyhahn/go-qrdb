//go:build codec_json && raft

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

package unix

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// stubEncryptionMigrator is a minimal service.EncryptionMigrator used to
// verify the WithEncryptionMigrator option and its Start-time registration.
type stubEncryptionMigrator struct{}

func (stubEncryptionMigrator) EnableEncryption(_ context.Context, _ service.BarrierScope, _ seal.MigrationOptions) error {
	return nil
}

var _ service.EncryptionMigrator = stubEncryptionMigrator{}

// TestWithLock_Applied verifies the WithLock option sets the lockSvc field.
func TestWithLock_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-lock.sock",
		WithLock(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.lockSvc != nil {
		t.Fatal("expected nil lockSvc")
	}
}

// TestWithTenantService_Applied verifies the WithTenantService option sets the tenantSvc field.
func TestWithTenantService_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-tenant.sock",
		WithTenantService(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.tenantSvc != nil {
		t.Fatal("expected nil tenantSvc")
	}
}

// TestWithEncryptionMigrator_Applied verifies the WithEncryptionMigrator
// option sets the encMigrator field. The nil case must not crash Start.
func TestWithEncryptionMigrator_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-encmigrator.sock",
		WithEncryptionMigrator(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.encMigrator != nil {
		t.Fatal("expected nil encMigrator")
	}
}

// TestWithEncryptionMigrator_NonNil verifies the WithEncryptionMigrator
// option propagates a non-nil migrator through to the Server struct.
func TestWithEncryptionMigrator_NonNil(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-encmigrator-nn.sock",
		WithEncryptionMigrator(stubEncryptionMigrator{}),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.encMigrator == nil {
		t.Fatal("expected non-nil encMigrator after WithEncryptionMigrator")
	}
}

// TestStart_WithEncryptionMigrator_Registered exercises the non-nil
// encMigrator branch in Start so that BarrierEnableEncryption is reachable
// over the Unix socket the same way it is over REST and TCP gRPC.
func TestStart_WithEncryptionMigrator_Registered(t *testing.T) {
	socketPath := tempSocketPath(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath,
		WithEncryptionMigrator(stubEncryptionMigrator{}),
		WithTLS(selfSignedTLSConfig(t)))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	if srv.Addr() == "" {
		t.Fatal("expected non-empty Addr after Start")
	}
}

// TestWithAuthFactorService_Applied verifies the WithAuthFactorService option sets the authFactorSvc field.
func TestWithAuthFactorService_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-authfactor.sock",
		WithAuthFactorService(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.authFactorSvc != nil {
		t.Fatal("expected nil authFactorSvc")
	}
}

// TestWithTenantBarrierService_Applied verifies the WithTenantBarrierService
// option sets the tenantBarrierSvc field. The nil case must not crash Start.
func TestWithTenantBarrierService_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-tenantbarrier.sock",
		WithTenantBarrierService(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.tenantBarrierSvc != nil {
		t.Fatal("expected nil tenantBarrierSvc")
	}
}

// TestWithTenantBarrierService_NonNil verifies the WithTenantBarrierService
// option propagates a non-nil tenant servicer through to the Server struct.
func TestWithTenantBarrierService_NonNil(t *testing.T) {
	stub := &stubTenantBarrierSvc{}
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-tenantbarrier-nn.sock",
		WithTenantBarrierService(stub),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.tenantBarrierSvc == nil {
		t.Fatal("expected non-nil tenantBarrierSvc after WithTenantBarrierService")
	}
}

// TestWithKVService_Applied verifies the WithKVService option overrides the kvSvc field.
func TestWithKVService_Applied(t *testing.T) {
	srv := NewServer(nil, nil, "/tmp/test-kvsvc.sock",
		WithKVService(newMockKVService()),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.kvSvc == nil {
		t.Fatal("expected non-nil kvSvc after WithKVService")
	}
}

// TestWithAdminService_Applied verifies the WithAdminService option overrides the adminSvc field.
func TestWithAdminService_Applied(t *testing.T) {
	srv := NewServer(nil, nil, "/tmp/test-adminsvc.sock",
		WithAdminService(&mockAdminService{}),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.adminSvc == nil {
		t.Fatal("expected non-nil adminSvc after WithAdminService")
	}
}

// TestWithAuditLogger_Applied verifies the WithAuditLogger option sets the auditLogger field.
func TestWithAuditLogger_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-audit.sock",
		WithAuditLogger(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.auditLogger != nil {
		t.Fatal("expected nil auditLogger")
	}
}

// TestWithAllNewOptions_StartStop exercises all newly-covered options in an
// integrated Start/Stop cycle.
func TestWithAllNewOptions_StartStop(t *testing.T) {
	socketPath := tempSocketPath(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath,
		WithLock(nil),
		WithTenantService(nil),
		WithAuthFactorService(nil),
		WithTenantBarrierService(nil),
		WithAuditLogger(nil),
		WithTLS(selfSignedTLSConfig(t)))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	if srv.Addr() != socketPath {
		t.Errorf("expected Addr %q, got %q", socketPath, srv.Addr())
	}
}

// TestStart_WithTenantBarrierService_Registered exercises the non-nil
// tenantBarrierSvc branch in Start so that the gRPC service registration is
// covered. This is the parallel of TestStart_WithAuditLogger but for the
// per-tenant barrier RPCs.
func TestStart_WithTenantBarrierService_Registered(t *testing.T) {
	socketPath := tempSocketPath(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath,
		WithTenantBarrierService(&stubTenantBarrierSvc{}),
		WithTLS(selfSignedTLSConfig(t)))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	if srv.Addr() == "" {
		t.Fatal("expected non-empty Addr after Start")
	}
}

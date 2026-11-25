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

	"github.com/jeremyhahn/go-qrdb/pkg/group"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// stubEncryptionMigrator is a minimal service.EncryptionMigrator used to
// verify the WithEncryptionMigrator option and its Start-time registration.
type stubEncryptionMigrator struct{}

func (stubEncryptionMigrator) EnableEncryption(_ context.Context, _ service.BarrierScope, _ seal.MigrationOptions) error {
	return nil
}

var _ service.EncryptionMigrator = stubEncryptionMigrator{}

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

type stubGroupService struct{ group.GroupServicer }

type stubVolumeService struct{ volume.Service }

type stubAwaitingAdminProbe struct{ awaiting bool }

func (p stubAwaitingAdminProbe) AwaitingAdminCredential() bool { return p.awaiting }

func TestStart_WithGroupVolumeAuditAndProbe_Registered(t *testing.T) {
	socketPath := tempSocketPath(t)
	groups := stubGroupService{}
	volumes := stubVolumeService{}
	replicated := service.NewOptInAuditLogger(nil, nil, false)
	probe := stubAwaitingAdminProbe{}
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath,
		WithGroupService(groups),
		WithVolumeService(volumes),
		WithReplicatedAuditLogger(replicated),
		WithAwaitingAdminCredentialProbe(probe),
		WithTLS(selfSignedTLSConfig(t)))

	if srv.groupSvc != groups {
		t.Fatal("WithGroupService did not set the group service")
	}
	if srv.volumeSvc != volumes {
		t.Fatal("WithVolumeService did not set the volume service")
	}
	if srv.replicatedAudit != replicated {
		t.Fatal("WithReplicatedAuditLogger did not set the replicated audit logger")
	}
	if srv.awaitingAdminCredentialProbe != probe {
		t.Fatal("WithAwaitingAdminCredentialProbe did not set the probe")
	}

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	if srv.Addr() == "" {
		t.Fatal("expected non-empty Addr after Start")
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

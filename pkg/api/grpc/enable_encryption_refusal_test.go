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

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// migrationRefusal stands in for a migrator's own typed refusal -- the shape
// sdk/go/node's MigrationNotSupportedError has -- which is NOT the sentinel
// itself but reports as it through errors.Is. A mapping that compared with ==
// instead of errors.Is would miss it.
type migrationRefusal struct{ reason string }

func (e *migrationRefusal) Error() string { return "migration not supported: " + e.reason }

func (e *migrationRefusal) Is(target error) bool {
	return target == service.ErrMigrationProviderUnavailable
}

// TestBarrierEnableEncryption_MigratorRefusalIsUnimplemented pins that a
// WIRED migrator declining the migration answers Unimplemented, as an unwired
// one does, rather than Internal. Unix serves this same gRPC service.
func TestBarrierEnableEncryption_MigratorRefusalIsUnimplemented(t *testing.T) {
	mig := &captureGRPCMigrator{ch: make(chan grpcMigrateCall, 1), err: &migrationRefusal{reason: "no seal store"}}
	client, cleanup := startSQLTestServer(t, WithEncryptionMigrator(mig))
	defer cleanup()

	_, err := client.BarrierEnableEncryption(context.Background(), &pb.BarrierEnableEncryptionRequest{
		Strategy: "passphrase", Passphrase: []byte("pw"),
	})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("code = %v, want Unimplemented (err=%v)", status.Code(err), err)
	}
	if got := ServiceErrorCode(&migrationRefusal{reason: "x"}); got != codes.Unimplemented {
		t.Fatalf("ServiceErrorCode = %v, want Unimplemented", got)
	}
}

// TestBarrierEnableEncryption_DataPlaneRecyclingIsUnavailable pins the
// retryable answer for a migration that arrives mid-recycle.
func TestBarrierEnableEncryption_DataPlaneRecyclingIsUnavailable(t *testing.T) {
	mig := &captureGRPCMigrator{ch: make(chan grpcMigrateCall, 1), err: &service.DataPlaneRecyclingError{Service: "tenant"}}
	client, cleanup := startSQLTestServer(t, WithEncryptionMigrator(mig))
	defer cleanup()

	_, err := client.BarrierEnableEncryption(context.Background(), &pb.BarrierEnableEncryptionRequest{
		TenantId: "acme", Strategy: "passphrase", Passphrase: []byte("pw"),
	})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("code = %v, want Unavailable (err=%v)", status.Code(err), err)
	}
}

// TestBarrierEnableEncryption_NodeClosedIsUnavailable pins that a migration
// refused because the node is closing is retryable, not Internal.
func TestBarrierEnableEncryption_NodeClosedIsUnavailable(t *testing.T) {
	mig := &captureGRPCMigrator{ch: make(chan grpcMigrateCall, 1), err: service.ErrNodeClosed}
	client, cleanup := startSQLTestServer(t, WithEncryptionMigrator(mig))
	defer cleanup()

	_, err := client.BarrierEnableEncryption(context.Background(), &pb.BarrierEnableEncryptionRequest{
		Strategy: "passphrase", Passphrase: []byte("pw"),
	})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("code = %v, want Unavailable (err=%v)", status.Code(err), err)
	}
	if got := ServiceErrorCode(service.ErrNodeClosed); got != codes.Unavailable {
		t.Fatalf("ServiceErrorCode = %v, want Unavailable", got)
	}
}

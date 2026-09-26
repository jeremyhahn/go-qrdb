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
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
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

// TestHandleBarrierEnableEncryption_MigratorRefusalIsUnimplemented pins that a
// WIRED migrator declining the migration reaches the client as
// StatusUnimplemented -- what ServiceNotConfiguredError already gives an
// unwired one -- and not the generic StatusError.
func TestHandleBarrierEnableEncryption_MigratorRefusalIsUnimplemented(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "the shared sentinel", err: service.ErrMigrationProviderUnavailable},
		{name: "a typed refusal matching it", err: &migrationRefusal{reason: "no seal store"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mig := &captureQUICMigrator{ch: make(chan quicMigrateCall, 1), err: tt.err}
			srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithEncryptionMigrator(mig))
			payload, _ := proto.Marshal(&pb.BarrierEnableEncryptionRequest{Strategy: "passphrase", Passphrase: []byte("pw")})

			_, err := srv.handleBarrierEnableEncryption(context.Background(), payload)
			if err == nil {
				t.Fatal("expected the migrator's refusal")
			}
			if status, _ := mapServiceError(err); status != wire.StatusUnimplemented {
				t.Fatalf("status = %s, want UNIMPLEMENTED", wire.StatusName(status))
			}
		})
	}
}

// TestHandleBarrierEnableEncryption_DataPlaneRecyclingIsUnavailable pins the
// retryable answer for a migration that arrives mid-recycle.
func TestHandleBarrierEnableEncryption_DataPlaneRecyclingIsUnavailable(t *testing.T) {
	mig := &captureQUICMigrator{ch: make(chan quicMigrateCall, 1), err: &service.DataPlaneRecyclingError{Service: "tenant"}}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithEncryptionMigrator(mig))
	payload, _ := proto.Marshal(&pb.BarrierEnableEncryptionRequest{TenantId: "acme", Strategy: "passphrase", Passphrase: []byte("pw")})

	_, err := srv.handleBarrierEnableEncryption(context.Background(), payload)
	if status, _ := mapServiceError(err); status != wire.StatusUnavailable {
		t.Fatalf("status = %s, want UNAVAILABLE", wire.StatusName(status))
	}
}

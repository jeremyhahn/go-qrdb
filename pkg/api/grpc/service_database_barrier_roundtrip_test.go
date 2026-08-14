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

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// barrierRoundtripMock implements only the DatabaseServicer methods needed
// to prove the gRPC barrier metadata round trip: GetForTenant (response
// path) and UpdateBarrierForTenant (request path). It records the
// DatabaseBarrierConfig it receives from UpdateBarrierForTenant.
type barrierRoundtripMock struct {
	service.DatabaseServicer

	getResult *database.DatabaseInfo
	gotUpdate *database.DatabaseBarrierConfig
}

func (m *barrierRoundtripMock) GetForTenant(_ context.Context, _ string) (*database.DatabaseInfo, error) {
	return m.getResult, nil
}

func (m *barrierRoundtripMock) UpdateBarrierForTenant(_ context.Context, name string, barrier *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) {
	m.gotUpdate = barrier
	return &database.DatabaseInfo{Name: name, Barrier: barrier}, nil
}

// TestDatabaseGet_BarrierMetadata_RoundTrips is the MEDIUM #9 proof for the
// response path: Mode/SharesRequired/SharesTotal set on the domain
// DatabaseInfo must survive translation into the protobuf response, not
// just Enabled.
func TestDatabaseGet_BarrierMetadata_RoundTrips(t *testing.T) {
	mock := &barrierRoundtripMock{
		getResult: &database.DatabaseInfo{
			Name: "barrier-meta-db",
			Barrier: &database.DatabaseBarrierConfig{
				Enabled:        true,
				Mode:           "passphrase",
				SharesRequired: 3,
				SharesTotal:    5,
			},
		},
	}
	client, cleanup := startSQLTestServer(t, WithDatabaseService(mock))
	defer cleanup()

	resp, err := client.DatabaseGet(context.Background(), &pb.DatabaseGetRequest{Name: "barrier-meta-db"})
	if err != nil {
		t.Fatalf("DatabaseGet failed: %v", err)
	}
	if resp.GetBarrier() == nil {
		t.Fatal("DatabaseGet response Barrier = nil, want non-nil")
	}
	if !resp.GetBarrier().GetEnabled() {
		t.Error("Barrier.Enabled = false, want true")
	}
	if resp.GetBarrier().GetMode() != "passphrase" {
		t.Errorf("Barrier.Mode = %q, want %q", resp.GetBarrier().GetMode(), "passphrase")
	}
	if resp.GetBarrier().GetSharesRequired() != 3 {
		t.Errorf("Barrier.SharesRequired = %d, want 3", resp.GetBarrier().GetSharesRequired())
	}
	if resp.GetBarrier().GetSharesTotal() != 5 {
		t.Errorf("Barrier.SharesTotal = %d, want 5", resp.GetBarrier().GetSharesTotal())
	}
}

// TestDatabaseUpdateBarrier_MetadataFields_RoundTrip is the MEDIUM #9 proof
// for the request path: Mode/SharesRequired/SharesTotal sent on the wire
// must reach UpdateBarrierForTenant's DatabaseBarrierConfig, not just
// Enabled -- otherwise a read-modify-write client that only round-trips
// Enabled would silently erase the persisted mode/shares metadata.
func TestDatabaseUpdateBarrier_MetadataFields_RoundTrip(t *testing.T) {
	mock := &barrierRoundtripMock{}
	client, cleanup := startSQLTestServer(t, WithDatabaseService(mock))
	defer cleanup()

	_, err := client.DatabaseUpdateBarrier(context.Background(), &pb.DatabaseUpdateBarrierRequest{
		Name: "barrier-meta-db",
		Barrier: &pb.DatabaseBarrierConfig{
			Enabled:        true,
			Mode:           "passphrase",
			SharesRequired: 2,
			SharesTotal:    3,
		},
	})
	if err != nil {
		t.Fatalf("DatabaseUpdateBarrier failed: %v", err)
	}
	if mock.gotUpdate == nil {
		t.Fatal("UpdateBarrierForTenant was not called with a barrier config")
	}
	if !mock.gotUpdate.Enabled {
		t.Error("gotUpdate.Enabled = false, want true")
	}
	if mock.gotUpdate.Mode != "passphrase" {
		t.Errorf("gotUpdate.Mode = %q, want %q", mock.gotUpdate.Mode, "passphrase")
	}
	if mock.gotUpdate.SharesRequired != 2 {
		t.Errorf("gotUpdate.SharesRequired = %d, want 2", mock.gotUpdate.SharesRequired)
	}
	if mock.gotUpdate.SharesTotal != 3 {
		t.Errorf("gotUpdate.SharesTotal = %d, want 3", mock.gotUpdate.SharesTotal)
	}
}

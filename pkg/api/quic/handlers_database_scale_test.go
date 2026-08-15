// Copyright (c) 2026 Jeremy Hahn
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

// Guard and validation tests for the database-scale handler. Scaling changes a
// database's replication factor, so every rejection here happens BEFORE the
// service is reached -- a malformed request must not become a partial
// membership change.

package quic

import (
	"context"
	"errors"
	"testing"
)

// TestHandleDatabaseScaleRefusesWithoutAService covers the nil-service guard.
func TestHandleDatabaseScaleRefusesWithoutAService(t *testing.T) {
	s := &Server{}

	raw, err := s.handleDatabaseScale(context.Background(), []byte(`{"name":"db1","target_replicas":3}`))
	if err == nil {
		t.Fatal("handleDatabaseScale must fail with no database service")
	}
	var want *ServiceNotConfiguredError
	if !errors.As(err, &want) {
		t.Fatalf("error = %v (%T), want *ServiceNotConfiguredError", err, err)
	}
	if want.Service != "database" {
		t.Errorf("Service = %q, want %q", want.Service, "database")
	}
	if raw != nil {
		t.Error("a refused scale must return no payload")
	}
}

// TestHandleDatabaseUpdateBarrierRefusesWithoutAService is the same guard on
// the sibling handler, which shares the service and the failure mode.
func TestHandleDatabaseUpdateBarrierRefusesWithoutAService(t *testing.T) {
	s := &Server{}

	raw, err := s.handleDatabaseUpdateBarrier(context.Background(), []byte(`{"name":"db1"}`))
	if err == nil {
		t.Fatal("handleDatabaseUpdateBarrier must fail with no database service")
	}
	var want *ServiceNotConfiguredError
	if !errors.As(err, &want) {
		t.Fatalf("error = %v (%T), want *ServiceNotConfiguredError", err, err)
	}
	if raw != nil {
		t.Error("a refused update must return no payload")
	}
}

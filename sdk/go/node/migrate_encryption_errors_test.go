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

package node

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestMigrationReopenError_IsNotARefusal pins the other half of the split: a
// failure inside a migration the node accepted must not be reported by any
// transport as "migration is not available on this node".
func TestMigrationReopenError_IsNotARefusal(t *testing.T) {
	tests := []struct {
		name      string
		err       *MigrationReopenError
		wantMsg   string
		wantCause error
	}{
		{
			name:    "no cause",
			err:     &MigrationReopenError{Reason: "retained boot params lost during migration"},
			wantMsg: "node: encryption migration reopen failed: retained boot params lost during migration",
		},
		{
			name:      "with cause",
			err:       &MigrationReopenError{Reason: "rebuild seal-window services after shamir migration", Err: seal.ErrSealed},
			wantMsg:   "node: encryption migration reopen failed: rebuild seal-window services after shamir migration: " + seal.ErrSealed.Error(),
			wantCause: seal.ErrSealed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.wantMsg {
				t.Fatalf("Error() = %q, want %q", got, tt.wantMsg)
			}
			if errors.Is(tt.err, service.ErrMigrationProviderUnavailable) {
				t.Fatal("a reopen failure must not match ErrMigrationProviderUnavailable")
			}
			if !errors.Is(tt.err.Unwrap(), tt.wantCause) || (tt.wantCause == nil && tt.err.Unwrap() != nil) {
				t.Fatalf("Unwrap() = %v, want %v", tt.err.Unwrap(), tt.wantCause)
			}
		})
	}
}

// TestNodeTenantResolver_MidRecycleIsRetryable pins that the resolver's answer
// while a system-scope migration has the data plane torn down is the
// temporary DataPlaneRecyclingError -- which every transport answers
// Unavailable -- and not a "cannot migrate here" refusal.
func TestNodeTenantResolver_MidRecycleIsRetryable(t *testing.T) {
	r := &nodeTenantResolver{node: &Node{}}

	checks := map[string]error{
		"CheckBarrierMode": r.CheckBarrierMode(context.Background(), "acme", seal.StrategyPassphrase),
	}
	_, err := r.GetTenant(context.Background(), "acme")
	checks["GetTenant"] = err

	for name, err := range checks {
		var recycling *service.DataPlaneRecyclingError
		if !errors.As(err, &recycling) {
			t.Fatalf("%s error = %T %v, want *service.DataPlaneRecyclingError", name, err, err)
		}
		if errors.Is(err, service.ErrMigrationProviderUnavailable) {
			t.Fatalf("%s: a mid-recycle error must not read as \"cannot migrate\"", name)
		}
	}
}

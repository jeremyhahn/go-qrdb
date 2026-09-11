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
	"fmt"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// This file carries no build constraint, matching errors.go, where
// mapServiceError lives.

// TestMapServiceError_TenantTeardownRefusals pins the wire statuses for the
// refusals the tenant teardown added (go-qrdb#413), so the QUIC transport
// agrees with REST's 409/409/400 and gRPC's AlreadyExists /
// FailedPrecondition / InvalidArgument rather than reporting a client error
// as a server fault.
func TestMapServiceError_TenantTeardownRefusals(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want uint16
	}{
		{
			name: "deleted id is not available",
			err:  &tenant.TenantDeletedError{TenantID: "acme", DeletedAt: time.Now().UTC()},
			want: wire.StatusAlreadyExists,
		},
		{
			name: "tenant still owns databases",
			err:  &tenant.TenantHasDatabasesError{TenantID: "acme", Databases: []string{"ledger"}},
			want: wire.StatusFailedPrecondition,
		},
		{
			name: "reserved id is a client error, not a server fault",
			err:  &tenant.ReservedTenantIDError{TenantID: "system", Reason: "reserved"},
			want: wire.StatusInvalidArgument,
		},
		{
			name: "reserved id wrapped by a caller",
			err:  fmt.Errorf("delete: %w", &tenant.ReservedTenantIDError{TenantID: "system", Reason: "reserved"}),
			want: wire.StatusInvalidArgument,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, msg := mapServiceError(tc.err)
			if got != tc.want {
				t.Errorf("mapServiceError(%v) = %d, want %d", tc.err, got, tc.want)
			}
			if len(msg) == 0 {
				t.Error("the refusal must carry a message the caller can act on")
			}
		})
	}
}

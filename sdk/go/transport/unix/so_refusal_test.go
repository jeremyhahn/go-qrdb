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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestSORefusals_RoundTripTyped: the server's separation-of-duties refusals
// reach the client as the typed errors sdk/go re-exports, the conflict's
// tenant included (go-qrdb#1129).
func TestSORefusals_RoundTripTyped(t *testing.T) {
	cases := []struct {
		name string
		err  error
		is   func(*testing.T, error)
	}{
		{"required", &auth.SORoleRequiredError{}, func(t *testing.T, err error) {
			var e *auth.SORoleRequiredError
			if !errors.As(err, &e) {
				t.Fatalf("Health error = %T %v, want *SORoleRequiredError", err, err)
			}
		}},
		{"conflict", &auth.SORoleConflictError{TenantID: "t1"}, func(t *testing.T, err error) {
			var e *auth.SORoleConflictError
			if !errors.As(err, &e) || e.TenantID != "t1" {
				t.Fatalf("Health error = %T %v, want *SORoleConflictError for tenant t1", err, err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adminSvc := newMockAdminService()
			adminSvc.healthErr = tc.err
			addr, stopSrv := startUnixTestServer(t, newMockKVService(), adminSvc)
			defer stopSrv()
			tr, err := New(transport.WithAddress(addr), testClientTLSOption())
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer tr.Close()
			if err := tr.Connect(context.Background()); err != nil {
				t.Fatalf("Connect: %v", err)
			}
			_, err = tr.Health(context.Background())
			tc.is(t, err)
		})
	}
}

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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestCreateTenant_LeavesCallerSOPinIntact pins that the embedded
// CreateTenant hands the onboarding service a copy of the SO PIN. The real
// service wipes the SOPIN it receives (secure.NewSecret), on success and on
// failure; the double does the same, so an aliased buffer would come back
// zeroed and a retry would send zeros.
func TestCreateTenant_LeavesCallerSOPinIntact(t *testing.T) {
	const pin = "so-pin-1234"
	refuse := errors.New("onboarding refused")
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"success", nil},
		{"refused", refuse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			onboarding := &mockOnboarding{onboardFn: func(_ context.Context, req *service.OnboardTenantRequest) (*service.OnboardTenantResult, error) {
				got = string(req.SOPIN)
				clear(req.SOPIN)
				if tc.err != nil {
					return nil, tc.err
				}
				return &service.OnboardTenantResult{Tenant: &tenant.TenantInfo{ID: req.ID, Name: req.Name}}, nil
			}}
			tr := newExtTransport(t, WithTenantOnboarding(onboarding))

			soPIN := transport.Secret(pin)
			_, err := tr.CreateTenant(context.Background(), &transport.CreateTenantRequest{
				ID: "acme", Name: "acme", AdminUsername: "admin", SOUsername: "so", SOPin: soPIN,
			})
			if !errors.Is(err, tc.err) {
				t.Fatalf("CreateTenant: got %v, want %v", err, tc.err)
			}
			if got != pin {
				t.Fatalf("onboarding received SO PIN %q, want %q", got, pin)
			}
			if string(soPIN) != pin {
				t.Fatalf("caller's SO PIN changed to %q after CreateTenant", soPIN)
			}
		})
	}
}

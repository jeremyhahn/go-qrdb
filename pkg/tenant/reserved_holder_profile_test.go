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

package tenant

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/holderprofile"
)

// The holder certificate profiles are reserved as tenant ids. A certificate
// issued before the ceremony CA set the tenant carries the profile as its
// FIRST OU, which the mTLS authenticator reads as the tenant; a tenant named
// after a profile would be the tenant those certificates authenticate in.

func TestValidateReservedTenantID_HolderProfiles(t *testing.T) {
	for _, id := range []string{holderprofile.Custodian, holderprofile.SecurityOfficer} {
		err := validateReservedTenantID(context.Background(), id)
		var rerr *ReservedTenantIDError
		if !errors.As(err, &rerr) {
			t.Fatalf("%s: expected *ReservedTenantIDError, got %T: %v", id, err, err)
		}
		if rerr.TenantID != id {
			t.Errorf("TenantID = %q, want %q", rerr.TenantID, id)
		}
	}
}

// TestValidateDeletableTenantID_HolderProfiles: the delete path consults the
// same set.
func TestValidateDeletableTenantID_HolderProfiles(t *testing.T) {
	for _, id := range []string{holderprofile.Custodian, holderprofile.SecurityOfficer} {
		var rerr *ReservedTenantIDError
		if err := validateDeletableTenantID(id); !errors.As(err, &rerr) {
			t.Errorf("%s: err = %v; want *ReservedTenantIDError", id, err)
		}
	}
}

// TestValidateReservedTenantID_NearHolderProfilesAreFree: only the exact
// profile names are reserved.
func TestValidateReservedTenantID_NearHolderProfilesAreFree(t *testing.T) {
	for _, id := range []string{"custodians", "acme-custodian", "security-officers"} {
		if err := validateReservedTenantID(context.Background(), id); err != nil {
			t.Errorf("%s: %v; want free", id, err)
		}
	}
}

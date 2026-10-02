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

package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// TestSOAuthenticator_VerifySOPINLocksOut: the per-tenant SO PIN is held to
// the same lockout as the node SO PIN (auth.SOPINLockout): after the free
// wrong PINs for a tenant, even the right PIN is refused with
// *auth.SOPINLockedError, without being compared, while another tenant is
// unaffected.
func TestSOAuthenticator_VerifySOPINLocksOut(t *testing.T) {
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "so", TenantID: "tenant-1"})
	a := newSOAuth(t)
	for _, tenant := range []string{"tenant-1", "tenant-2"} {
		if err := a.SetSOPIN(ctx, tenant, []byte("correct-horse-battery-staple")); err != nil {
			t.Fatalf("SetSOPIN %s: %v", tenant, err)
		}
	}
	for i := 0; i < auth.SOPINFreeFailures; i++ {
		if err := a.VerifySOPIN(ctx, "tenant-1", []byte("wrong-pin-0000")); !errors.Is(err, auth.ErrSOPINInvalid) {
			t.Fatalf("wrong PIN %d = %v, want ErrSOPINInvalid", i+1, err)
		}
	}
	var locked *auth.SOPINLockedError
	if err := a.VerifySOPIN(ctx, "tenant-1", []byte("correct-horse-battery-staple")); !errors.As(err, &locked) {
		t.Fatalf("the right PIN after the free failures = %v, want *auth.SOPINLockedError", err)
	}
	if err := a.VerifySOPIN(ctx, "tenant-2", []byte("correct-horse-battery-staple")); err != nil {
		t.Fatalf("another tenant's SO PIN = %v, want admitted", err)
	}
}

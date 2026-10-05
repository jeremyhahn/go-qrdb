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

package conformance

import (
	"reflect"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The user extension-field conformance matrix (go-qrdb#192, go-qrdb#197).
//
// RoleBindings, Attributes, Factors and the derived MFA fields were added to
// the User wire DTO on REST, gRPC, QUIC, MCP, unix and the embedded SDK in the
// same change. This suite is what makes a partial wiring impossible to ship
// silently: a field settable on four transports and dropped on two is a
// surface that disagrees with itself, invisible from inside any one of them.
//
// Each cell drives the full leave-alone / replace / clear cycle for
// RoleBindings and Attributes over ONE transport, and asserts that Factors and
// the derived MFA fields are present on the wire (never simply absent from the
// struct) and that Factors never carries a Data-shaped payload — there is no
// wire path to populate Factors at all, on any transport, so an empty result
// here is the ONLY correct one; the point is that the field exists in the
// response shape rather than being silently unmarshalable.
func TestUserExtensionFields_RoundTripOnEveryTransport(t *testing.T) {
	for _, tk := range userNotFoundTransports {
		t.Run(string(tk), func(t *testing.T) {
			c := bootUserCell(t, tk)
			ctx := c.callerCtx(t, tk)

			created, err := c.users.CreateUser(ctx, &transport.CreateUserRequest{
				Username:             "ext-fields-" + string(tk),
				TenantID:             tenantid.System,
				WithoutEmailLoginKey: true,
			})
			if err != nil {
				t.Fatalf("%s: CreateUser: %v", tk, err)
			}

			// A freshly created user carries none of the extension fields, and
			// the derived MFA fields report the empty state — never absent from
			// the struct, never true/non-empty with nothing to justify it.
			if created.MFAEnabled || len(created.MFAMethods) != 0 || created.MFAEnrolledAt != "" {
				t.Fatalf("%s: fresh user MFA state = enabled=%v methods=%v enrolledAt=%q, want all-empty",
					tk, created.MFAEnabled, created.MFAMethods, created.MFAEnrolledAt)
			}
			if len(created.Factors) != 0 {
				t.Fatalf("%s: fresh user Factors = %+v, want empty (no wire path ever populates it)", tk, created.Factors)
			}

			// REPLACE: both RoleBindings and Attributes. The bound role must be
			// a REAL role in tenant-a: go-qrdb#192 item 3 validates every
			// RoleBinding's RoleID against its OWN TenantID before the write
			// commits, so a fabricated identifier here would be refused by the
			// very check this suite is not testing.
			if _, err := c.rawRoles.CreateRole(ctx, &role.CreateRoleRequest{
				ID:       "role-a",
				Name:     "role-a",
				TenantID: "tenant-a",
			}); err != nil {
				t.Fatalf("%s: seed role-a in tenant-a: %v", tk, err)
			}
			wantBindings := []transport.RoleBinding{{RoleID: "role-a", TenantID: "tenant-a"}}
			wantAttrs := map[string]string{"department": "eng"}
			updated, err := c.users.UpdateUser(ctx, &transport.UpdateUserRequest{
				ID:           created.ID,
				RoleBindings: wantBindings,
				Attributes:   wantAttrs,
			})
			if err != nil {
				t.Fatalf("%s: UpdateUser(replace): %v", tk, err)
			}
			assertRoleBindings(t, tk, "replace", updated.RoleBindings, wantBindings)
			assertAttributes(t, tk, "replace", updated.Attributes, wantAttrs)

			// LEAVE ALONE: an update naming neither field must not disturb what
			// was just set — the nil-vs-empty contract every transport's wire
			// request carries for these two fields, matching AuthMethods.
			leftAlone, err := c.users.UpdateUser(ctx, &transport.UpdateUserRequest{
				ID:   created.ID,
				Name: "still ext-fields",
			})
			if err != nil {
				t.Fatalf("%s: UpdateUser(leave alone): %v", tk, err)
			}
			assertRoleBindings(t, tk, "leave-alone", leftAlone.RoleBindings, wantBindings)
			assertAttributes(t, tk, "leave-alone", leftAlone.Attributes, wantAttrs)

			// CLEAR: a non-nil empty value removes the set entirely.
			cleared, err := c.users.UpdateUser(ctx, &transport.UpdateUserRequest{
				ID:           created.ID,
				RoleBindings: []transport.RoleBinding{},
				Attributes:   map[string]string{},
			})
			if err != nil {
				t.Fatalf("%s: UpdateUser(clear): %v", tk, err)
			}
			if len(cleared.RoleBindings) != 0 {
				t.Errorf("%s: RoleBindings after clear = %+v, want empty", tk, cleared.RoleBindings)
			}
			if len(cleared.Attributes) != 0 {
				t.Errorf("%s: Attributes after clear = %+v, want empty", tk, cleared.Attributes)
			}

			// Re-fetch via GetUser: the clear must have actually persisted, not
			// merely been echoed back in the UpdateUser response.
			refetched, err := c.users.GetUser(ctx, created.ID)
			if err != nil {
				t.Fatalf("%s: GetUser after clear: %v", tk, err)
			}
			if len(refetched.RoleBindings) != 0 {
				t.Errorf("%s: GetUser RoleBindings after clear = %+v, want empty", tk, refetched.RoleBindings)
			}
			if len(refetched.Attributes) != 0 {
				t.Errorf("%s: GetUser Attributes after clear = %+v, want empty", tk, refetched.Attributes)
			}
		})
	}
}

// TestUserExtensionFields_RealFactorMetadataAndMFAAcrossEveryTransport closes
// the review finding that the round-trip suite above only ever exercised a
// FACTORLESS user: asserting all-empty values there is satisfied identically
// whether the projection works or was deleted outright, so it could not have
// caught a regression in the metadata/MFA wiring itself.
//
// Factors has no wire path on ANY transport (deliberately, per the ruling —
// enrollment is its own verb, never CreateUser/UpdateUser), so the only way
// to seed a REAL enrollment for this test is the same way seedIdentityFixtures
// seeds its role and user fixtures: directly against the in-process domain
// store the server under test actually reads from. Every transport's client
// then reads it back over its own wire, so a metadata field or an MFA
// derivation broken on any ONE of the six transports fails in that
// transport's subtest specifically.
func TestUserExtensionFields_RealFactorMetadataAndMFAAcrossEveryTransport(t *testing.T) {
	const sentinelData = `{"secret":"do-not-leak"}`
	enrolledAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC).Truncate(time.Second)

	for _, tk := range userNotFoundTransports {
		t.Run(string(tk), func(t *testing.T) {
			c := bootUserCell(t, tk)
			ctx := c.callerCtx(t, tk)

			created, err := c.users.CreateUser(ctx, &transport.CreateUserRequest{
				Username:             "real-factor-" + string(tk),
				TenantID:             tenantid.System,
				WithoutEmailLoginKey: true,
			})
			if err != nil {
				t.Fatalf("%s: CreateUser: %v", tk, err)
			}

			// Seed a REAL enrollment directly against the auth-factor service
			// that is the sole source of truth for Factors metadata and MFA
			// state (pkg/user.FactorLister, computed at read time). This is the
			// one write no transport's own client can perform (Factors is
			// read-only everywhere), so it goes through rawUserAuthFactors
			// rather than c.users -- exactly as seedIdentityFixtures seeds its
			// own fixtures in-process rather than through a transport call.
			if err := c.rawUserAuthFactors.Add(ctx, created.ID, &auth.AuthFactor{
				ID:           "f1",
				Type:         auth.AuthFactorType("fido2"),
				Credential:   []byte(sentinelData),
				RegisteredAt: enrolledAt,
			}); err != nil {
				t.Fatalf("%s: seed a real factor enrollment: %v", tk, err)
			}

			got, err := c.users.GetUser(ctx, created.ID)
			if err != nil {
				t.Fatalf("%s: GetUser: %v", tk, err)
			}

			// Metadata: present, correct, and — because transport.FactorMetadata
			// structurally has no field to carry it — the Data blob cannot be
			// represented in what came back, regardless of what this transport's
			// wire encoding looked like in flight.
			if len(got.Factors) != 1 {
				t.Fatalf("%s: Factors = %+v, want exactly one enrolled factor", tk, got.Factors)
			}
			f := got.Factors[0]
			if f.ID != "f1" {
				t.Errorf("%s: Factors[0].ID = %q, want f1", tk, f.ID)
			}
			if f.FactorName != "fido2" {
				t.Errorf("%s: Factors[0].FactorName = %q, want fido2", tk, f.FactorName)
			}
			if f.CreatedAt == "" {
				t.Errorf("%s: Factors[0].CreatedAt is empty, want the enrollment time", tk)
			} else if parsed, perr := time.Parse(time.RFC3339, f.CreatedAt); perr != nil || !parsed.Equal(enrolledAt) {
				t.Errorf("%s: Factors[0].CreatedAt = %q, want %s", tk, f.CreatedAt, enrolledAt.Format(time.RFC3339))
			}

			// MFA: derived from the factor that was just enrolled, not stored
			// separately -- a stale or hand-set value would not track the
			// enrollment at all, so this specifically exercises the derivation
			// running on the server that answers THIS transport.
			if !got.MFAEnabled {
				t.Errorf("%s: MFAEnabled = false, want true (one factor enrolled)", tk)
			}
			if len(got.MFAMethods) != 1 || got.MFAMethods[0] != "fido2" {
				t.Errorf("%s: MFAMethods = %v, want [fido2]", tk, got.MFAMethods)
			}
			if got.MFAEnrolledAt == "" {
				t.Errorf("%s: MFAEnrolledAt is empty, want the factor's enrollment time", tk)
			} else if parsed, perr := time.Parse(time.RFC3339, got.MFAEnrolledAt); perr != nil || !parsed.Equal(enrolledAt) {
				t.Errorf("%s: MFAEnrolledAt = %q, want %s", tk, got.MFAEnrolledAt, enrolledAt.Format(time.RFC3339))
			}

			// ListUsers must agree with GetUser -- a projection wired into one
			// read path and not the other is exactly the kind of partial fix
			// this suite exists to catch.
			listed, err := c.users.ListUsers(ctx, &transport.ListUsersRequest{TenantID: tenantid.System, PageSize: 200})
			if err != nil {
				t.Fatalf("%s: ListUsers: %v", tk, err)
			}
			var found *transport.User
			for _, u := range listed.Users {
				if u.ID == created.ID {
					found = u
					break
				}
			}
			if found == nil {
				t.Fatalf("%s: ListUsers did not include the seeded user", tk)
			}
			if !found.MFAEnabled || len(found.Factors) != 1 {
				t.Errorf("%s: ListUsers entry = %+v, want the same Factors/MFA GetUser reported", tk, found)
			}
		})
	}
}

// assertRoleBindings compares role bindings ignoring order, since no transport
// promises to preserve insertion order for this set.
func assertRoleBindings(t *testing.T, tk transportKind, phase string, got, want []transport.RoleBinding) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s (%s): RoleBindings = %+v, want %+v", tk, phase, got, want)
	}
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s (%s): RoleBindings = %+v, missing %+v", tk, phase, got, w)
		}
	}
}

func assertAttributes(t *testing.T, tk transportKind, phase string, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s (%s): Attributes = %+v, want %+v", tk, phase, got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s (%s): Attributes[%q] = %q, want %q", tk, phase, k, got[k], v)
		}
	}
}

// TestUserExtensionFields_FactorsAndSystemNeverOnTheWire is the fail-closed
// half of go-qrdb#106's and go-qrdb#192/#197's rulings: neither
// Factors[].Data nor System is ever settable through a create or update
// request, on any transport. sdk/go/transport is the ONE request shape shared
// by all six SDK transports (each transport package builds its own wire
// format FROM these types), so asserting the field set here covers every
// transport at once.
//
// A keyed composite literal naming every field it expects does NOT prove
// absence — Go happily compiles `T{A: 1}` for a T that also has an unlisted
// field B, silently zero-valued. This walks the actual field set by
// reflection instead, which fails the moment "factors" or "system" is added
// to either struct, whether or not the field was ever referenced.
func TestUserExtensionFields_FactorsAndSystemNeverOnTheWire(t *testing.T) {
	forbidden := []string{"Factors", "System"}

	assertNoField := func(v any) {
		t.Helper()
		typ := reflect.TypeOf(v)
		for i := 0; i < typ.NumField(); i++ {
			name := typ.Field(i).Name
			for _, f := range forbidden {
				if name == f {
					t.Errorf("%s.%s exists: a create/update request must never be able to set System "+
						"or the Factors Data blob (go-qrdb#106, go-qrdb#192/#197)", typ.Name(), name)
				}
			}
		}
	}

	assertNoField(transport.CreateUserRequest{})
	assertNoField(transport.UpdateUserRequest{})
}

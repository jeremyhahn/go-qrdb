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
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// The RoleBindings/Attributes/Factors/MFA wire projection (go-qrdb#192,
// go-qrdb#197). RoleBindings and Attributes are settable on UpdateUser
// through the same has_* nil-vs-empty contract AuthMethods already uses;
// Factors and the derived MFA fields are read-only and appear only on the
// response.

func TestUpdateUser_RoleBindingsHasFlagCarriesClearVersusLeaveAlone(t *testing.T) {
	cases := []struct {
		name        string
		req         *pb.UpdateUserProtoRequest
		wantNil     bool
		wantCleared bool
		wantLen     int
	}{
		{name: "no flag leaves alone", req: &pb.UpdateUserProtoRequest{Id: 1}, wantNil: true},
		{
			name: "values without the flag are ignored",
			req: &pb.UpdateUserProtoRequest{Id: 1,
				RoleBindings: []*pb.RoleBinding{{RoleId: "role-a", TenantId: "acme"}}},
			wantNil: true,
		},
		{name: "flag with no values clears", req: &pb.UpdateUserProtoRequest{Id: 1, HasRoleBindings: true}, wantCleared: true},
		{
			name: "flag with values replaces",
			req: &pb.UpdateUserProtoRequest{Id: 1, HasRoleBindings: true,
				RoleBindings: []*pb.RoleBinding{{RoleId: "role-a", TenantId: "acme"}}},
			wantLen: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &mockUserService{updateResult: &user.User{ID: 1}}
			s := &qrdbServiceServer{userSvc: svc}
			if _, err := s.UpdateUser(context.Background(), tc.req); err != nil {
				t.Fatalf("UpdateUser: %v", err)
			}
			got := svc.lastUpdateReq.RoleBindings
			switch {
			case tc.wantNil:
				if got != nil {
					t.Fatalf("RoleBindings = %#v, want nil (leave alone)", got)
				}
			case tc.wantCleared:
				if got == nil || len(got) != 0 {
					t.Fatalf("RoleBindings = %#v, want a non-nil empty slice (clear)", got)
				}
			default:
				if len(got) != tc.wantLen || got[0].RoleID != "role-a" || got[0].TenantID != "acme" {
					t.Fatalf("RoleBindings = %#v, want one {role-a acme}", got)
				}
			}
		})
	}
}

func TestUpdateUser_AttributesHasFlagCarriesClearVersusLeaveAlone(t *testing.T) {
	cases := []struct {
		name        string
		req         *pb.UpdateUserProtoRequest
		wantNil     bool
		wantCleared bool
		wantValue   string
	}{
		{name: "no flag leaves alone", req: &pb.UpdateUserProtoRequest{Id: 1}, wantNil: true},
		{
			name:    "values without the flag are ignored",
			req:     &pb.UpdateUserProtoRequest{Id: 1, Attributes: map[string]string{"department": "eng"}},
			wantNil: true,
		},
		{name: "flag with no values clears", req: &pb.UpdateUserProtoRequest{Id: 1, HasAttributes: true}, wantCleared: true},
		{
			name:      "flag with values replaces",
			req:       &pb.UpdateUserProtoRequest{Id: 1, HasAttributes: true, Attributes: map[string]string{"department": "eng"}},
			wantValue: "eng",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &mockUserService{updateResult: &user.User{ID: 1}}
			s := &qrdbServiceServer{userSvc: svc}
			if _, err := s.UpdateUser(context.Background(), tc.req); err != nil {
				t.Fatalf("UpdateUser: %v", err)
			}
			got := svc.lastUpdateReq.Attributes
			switch {
			case tc.wantNil:
				if got != nil {
					t.Fatalf("Attributes = %#v, want nil (leave alone)", got)
				}
			case tc.wantCleared:
				if got == nil || len(got) != 0 {
					t.Fatalf("Attributes = %#v, want a non-nil empty map (clear)", got)
				}
			default:
				if got["department"] != tc.wantValue {
					t.Fatalf("Attributes = %#v, want department=%q", got, tc.wantValue)
				}
			}
		})
	}
}

// TestUserToProto_ProjectsExtensionFieldsAndOmitsFactorData asserts the
// response side: GetUser must carry RoleBindings, Attributes, Factors
// METADATA ONLY and the derived MFA fields, and Factors[].Data must never
// reach the wire.
func TestUserToProto_ProjectsExtensionFieldsAndOmitsFactorData(t *testing.T) {
	enrolledAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	svc := &mockUserService{getResult: &user.User{
		ID:           1,
		RoleBindings: []user.RoleBinding{{RoleID: "role-a", TenantID: "acme"}},
		Attributes:   map[string]string{"department": "eng"},
		Factors: []user.FactorEnrollment{
			{ID: "f1", FactorName: "fido2", Data: []byte(`{"secret":"do-not-leak"}`), CreatedAt: enrolledAt},
		},
	}}
	s := &qrdbServiceServer{userSvc: svc}

	resp, err := s.GetUser(context.Background(), &pb.GetUserRequest{Id: 1})
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}

	if len(resp.GetRoleBindings()) != 1 || resp.GetRoleBindings()[0].GetRoleId() != "role-a" {
		t.Errorf("RoleBindings = %+v, want one {role-a acme}", resp.GetRoleBindings())
	}
	if resp.GetAttributes()["department"] != "eng" {
		t.Errorf("Attributes = %+v, want department=eng", resp.GetAttributes())
	}
	if len(resp.GetFactors()) != 1 || resp.GetFactors()[0].GetId() != "f1" || resp.GetFactors()[0].GetFactorName() != "fido2" {
		t.Errorf("Factors = %+v, want one {f1 fido2 ...}", resp.GetFactors())
	}
	if !resp.GetMfaEnabled() {
		t.Error("MfaEnabled = false, want true")
	}
	if len(resp.GetMfaMethods()) != 1 || resp.GetMfaMethods()[0] != "fido2" {
		t.Errorf("MfaMethods = %v, want [fido2]", resp.GetMfaMethods())
	}
	if resp.GetMfaEnrolledAt() == "" {
		t.Error("MfaEnrolledAt is empty, want the factor's enrollment time")
	}

	// The Data blob must never be reachable through the proto message: there
	// is no field on pb.FactorMetadata for it, so this is a structural
	// guarantee, verified here by confirming what the getter surface exposes.
	f := resp.GetFactors()[0]
	if f.GetId() == "" || f.GetFactorName() == "" {
		t.Fatal("factor metadata missing")
	}
}

func TestUserToProto_NoExtensionFieldsOnBareUser(t *testing.T) {
	svc := &mockUserService{getResult: &user.User{ID: 1}}
	s := &qrdbServiceServer{userSvc: svc}

	resp, err := s.GetUser(context.Background(), &pb.GetUserRequest{Id: 1})
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if resp.GetMfaEnabled() || len(resp.GetMfaMethods()) != 0 || resp.GetMfaEnrolledAt() != "" {
		t.Errorf("MFA state = enabled=%v methods=%v enrolledAt=%q, want all-empty",
			resp.GetMfaEnabled(), resp.GetMfaMethods(), resp.GetMfaEnrolledAt())
	}
	if len(resp.GetRoleBindings()) != 0 || len(resp.GetAttributes()) != 0 || len(resp.GetFactors()) != 0 {
		t.Errorf("extension fields present on a bare user: role_bindings=%v attributes=%v factors=%v",
			resp.GetRoleBindings(), resp.GetAttributes(), resp.GetFactors())
	}
}

func TestRoleBindingsToProto_NoneIsNil(t *testing.T) {
	if got := roleBindingsToProto(nil); got != nil {
		t.Errorf("roleBindingsToProto(nil) = %v, want nil", got)
	}
}

func TestProtoToRoleBindings_Empty(t *testing.T) {
	got := protoToRoleBindings(nil)
	if got == nil || len(got) != 0 {
		t.Errorf("protoToRoleBindings(nil) = %#v, want a non-nil empty slice", got)
	}
}

func TestFactorsToProto_NoneIsNil(t *testing.T) {
	if got := factorsToProto(nil); got != nil {
		t.Errorf("factorsToProto(nil) = %v, want nil", got)
	}
}

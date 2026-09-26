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

// RemoveCertBinding — the withdrawal verb on the gRPC surface, which the unix
// socket transport serves too (go-qrdb#518).

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestRemoveCertBinding_CarriesUserIDAndThumbprint is the reachability
// assertion: both request fields arrive at the service intact.
func TestRemoveCertBinding_CarriesUserIDAndThumbprint(t *testing.T) {
	const thumbprint = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var gotID uint64
	var gotThumbprint string
	svc := &mockUserService{
		removeCertBindingFn: func(_ context.Context, userID uint64, tp string) (*user.User, error) {
			gotID, gotThumbprint = userID, tp
			return &user.User{ID: userID, Username: "alice", TenantID: "acme"}, nil
		},
	}
	s := &qrdbServiceServer{userSvc: svc}

	resp, err := s.RemoveCertBinding(context.Background(), &pb.RemoveCertBindingRequest{
		UserId:     1001,
		Thumbprint: thumbprint,
	})
	if err != nil {
		t.Fatalf("RemoveCertBinding: %v", err)
	}
	if gotID != 1001 {
		t.Fatalf("userID = %d, want 1001", gotID)
	}
	if gotThumbprint != thumbprint {
		t.Fatalf("thumbprint = %q, want %q", gotThumbprint, thumbprint)
	}
	if resp.GetId() != 1001 {
		t.Fatalf("response id = %d, want 1001", resp.GetId())
	}
}

// TestRemoveCertBinding_NilServiceIsUnimplemented refuses when no user service
// is wired, matching every other user RPC on this server.
func TestRemoveCertBinding_NilServiceIsUnimplemented(t *testing.T) {
	s := &qrdbServiceServer{}

	_, err := s.RemoveCertBinding(context.Background(), &pb.RemoveCertBindingRequest{
		UserId:     1001,
		Thumbprint: "AAAA",
	})
	if st, _ := status.FromError(err); st.Code() != codes.Unimplemented {
		t.Fatalf("code = %s, want Unimplemented", st.Code())
	}
}

// TestRemoveCertBinding_UnheldBindingIsNotFound pins the refusal's code. A
// withdrawal that removed nothing is NotFound, not OK: an operator who
// mistyped a thumbprint must not read a success.
func TestRemoveCertBinding_UnheldBindingIsNotFound(t *testing.T) {
	svc := &mockUserService{
		removeCertBindingFn: func(_ context.Context, userID uint64, tp string) (*user.User, error) {
			return nil, &user.CertBindingNotFoundError{UserID: userID, Thumbprint: tp}
		},
	}
	s := &qrdbServiceServer{userSvc: svc}

	_, err := s.RemoveCertBinding(context.Background(), &pb.RemoveCertBindingRequest{
		UserId:     1001,
		Thumbprint: "AAAA",
	})
	if st, _ := status.FromError(err); st.Code() != codes.NotFound {
		t.Fatalf("code = %s, want NotFound", st.Code())
	}
}

// TestRemoveCertBinding_ConcurrentUpdateIsAborted covers the exhausted-retry
// answer: Aborted is gRPC's designated code for a failed optimistic-concurrency
// check, which tells the client to re-read and retry rather than that the
// server broke.
func TestRemoveCertBinding_ConcurrentUpdateIsAborted(t *testing.T) {
	svc := &mockUserService{
		removeCertBindingFn: func(_ context.Context, userID uint64, _ string) (*user.User, error) {
			return nil, &user.ConcurrentUpdateError{ID: userID}
		},
	}
	s := &qrdbServiceServer{userSvc: svc}

	_, err := s.RemoveCertBinding(context.Background(), &pb.RemoveCertBindingRequest{
		UserId:     1001,
		Thumbprint: "AAAA",
	})
	if st, _ := status.FromError(err); st.Code() != codes.Aborted {
		t.Fatalf("code = %s, want Aborted", st.Code())
	}
}

// TestAddCertBinding_HijackAttemptIsAlreadyExists pins the code of the store's
// most security-relevant refusal: a thumbprint already bound to ANOTHER user
// is AlreadyExists, not Internal (go-qrdb#536), and the message names the
// user that holds it.
func TestAddCertBinding_HijackAttemptIsAlreadyExists(t *testing.T) {
	svc := &mockUserService{
		addCertBindingFn: func(_ context.Context, userID uint64, tp string, _ int64) (*user.User, error) {
			return nil, &user.CertBindingConflictError{Thumbprint: tp, BoundUserID: 7, TargetUserID: userID}
		},
	}
	s := &qrdbServiceServer{userSvc: svc}

	_, err := s.AddCertBinding(context.Background(), &pb.AddCertBindingRequest{
		UserId:     1001,
		Thumbprint: "AAAA",
	})
	st, _ := status.FromError(err)
	if st.Code() != codes.AlreadyExists {
		t.Fatalf("code = %s, want AlreadyExists", st.Code())
	}
	if !strings.Contains(st.Message(), "already bound to user 7") {
		t.Fatalf("message must name the user that holds the thumbprint, got %q", st.Message())
	}
}

// TestUserVerbs_BootstrapAdminLockoutIsFailedPrecondition (go-qrdb#517): the
// store's refusal of a change that would lock the bootstrap admin out is
// FailedPrecondition on every user verb that can make one, as REST's 409.
func TestUserVerbs_BootstrapAdminLockoutIsFailedPrecondition(t *testing.T) {
	lockout := func(reason user.AdminLockoutReason) error {
		return &user.BootstrapAdminLockoutError{UserID: 1001, Reason: reason}
	}
	ctx := context.Background()
	cases := map[string]func() error{
		"withdraw the last binding": func() error {
			s := &qrdbServiceServer{userSvc: &mockUserService{
				removeCertBindingFn: func(context.Context, uint64, string) (*user.User, error) {
					return nil, lockout(user.AdminLockoutNoLiveBinding)
				},
			}}
			_, err := s.RemoveCertBinding(ctx, &pb.RemoveCertBindingRequest{UserId: 1001, Thumbprint: "AAAA"})
			return err
		},
		"delete the admin": func() error {
			s := &qrdbServiceServer{userSvc: &mockUserService{deleteErr: lockout(user.AdminLockoutDelete)}}
			_, err := s.DeleteUser(ctx, &pb.DeleteUserRequest{Id: 1001})
			return err
		},
		"disable the admin": func() error {
			s := &qrdbServiceServer{userSvc: &mockUserService{disableErr: lockout(user.AdminLockoutLifecycle)}}
			_, err := s.DisableUser(ctx, &pb.DisableUserRequest{Id: 1001})
			return err
		},
		"rename the admin": func() error {
			s := &qrdbServiceServer{userSvc: &mockUserService{updateErr: lockout(user.AdminLockoutRename)}}
			_, err := s.UpdateUser(ctx, &pb.UpdateUserProtoRequest{Id: 1001, Username: "root"})
			return err
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			st, _ := status.FromError(call())
			if st.Code() != codes.FailedPrecondition {
				t.Fatalf("code = %s, want FailedPrecondition", st.Code())
			}
			if !strings.Contains(st.Message(), "bootstrap admin") {
				t.Fatalf("message %q does not carry the refusal", st.Message())
			}
		})
	}
}

// TestAddCertBinding_PastExpiryIsInvalidArgument (go-qrdb#517): an enrolment
// whose expiry has already passed is a bad argument, not an internal fault.
func TestAddCertBinding_PastExpiryIsInvalidArgument(t *testing.T) {
	svc := &mockUserService{
		addCertBindingFn: func(_ context.Context, _ uint64, tp string, notAfter int64) (*user.User, error) {
			return nil, &user.CertBindingExpiredError{Thumbprint: tp, NotAfter: time.Unix(notAfter, 0).UTC()}
		},
	}
	s := &qrdbServiceServer{userSvc: svc}
	_, err := s.AddCertBinding(context.Background(), &pb.AddCertBindingRequest{UserId: 1001, Thumbprint: "AAAA", NotAfterUnix: 1})
	if st, _ := status.FromError(err); st.Code() != codes.InvalidArgument {
		t.Fatalf("code = %s, want InvalidArgument", st.Code())
	}
}

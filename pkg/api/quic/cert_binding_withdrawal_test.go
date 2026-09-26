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

// user.RemoveCertBinding — the withdrawal verb on the native QUIC surface
// (go-qrdb#518).

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestHandleRemoveCertBinding_CarriesUserIDAndThumbprint is the reachability
// assertion: the wire payload's two fields arrive at the service intact.
func TestHandleRemoveCertBinding_CarriesUserIDAndThumbprint(t *testing.T) {
	const thumbprint = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var gotID uint64
	var gotThumbprint string
	svc := &mockUserService{
		removeCertBindingFn: func(_ context.Context, userID uint64, tp string) (*user.User, error) {
			gotID, gotThumbprint = userID, tp
			return &user.User{ID: userID, Username: "alice"}, nil
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithUserService(svc))

	payload, err := json.Marshal(removeCertBindingWireRequest{UserID: 1001, Thumbprint: thumbprint})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	out, err := srv.handleRemoveCertBinding(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleRemoveCertBinding: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("handleRemoveCertBinding returned an empty response")
	}
	if gotID != 1001 {
		t.Fatalf("userID = %d, want 1001", gotID)
	}
	if gotThumbprint != thumbprint {
		t.Fatalf("thumbprint = %q, want %q", gotThumbprint, thumbprint)
	}
}

// TestHandleRemoveCertBinding_NilService refuses when no user service is
// wired, rather than panicking on a nil interface.
func TestHandleRemoveCertBinding_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, err := json.Marshal(removeCertBindingWireRequest{UserID: 1, Thumbprint: "AAAA"})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	_, err = srv.handleRemoveCertBinding(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("err = %T(%v), want *ServiceNotConfiguredError", err, err)
	}
}

// TestHandleRemoveCertBinding_InvalidPayload rejects a malformed frame rather
// than withdrawing a zero-valued binding from user zero.
func TestHandleRemoveCertBinding_InvalidPayload(t *testing.T) {
	var called bool
	svc := &mockUserService{
		removeCertBindingFn: func(_ context.Context, _ uint64, _ string) (*user.User, error) {
			called = true
			return nil, nil
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithUserService(svc))

	_, err := srv.handleRemoveCertBinding(context.Background(), []byte("not-json"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %T(%v), want *UnmarshalError", err, err)
	}
	if called {
		t.Fatal("a malformed frame reached the service")
	}
}

// TestHandleRemoveCertBinding_PropagatesTheServiceRefusal keeps the store's
// typed refusal on the wire. A withdrawal that removed nothing must not be
// reported as a success by the transport.
func TestHandleRemoveCertBinding_PropagatesTheServiceRefusal(t *testing.T) {
	svc := &mockUserService{
		removeCertBindingFn: func(_ context.Context, userID uint64, tp string) (*user.User, error) {
			return nil, &user.CertBindingNotFoundError{UserID: userID, Thumbprint: tp}
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithUserService(svc))

	payload, err := json.Marshal(removeCertBindingWireRequest{UserID: 1001, Thumbprint: "AAAA"})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	_, err = srv.handleRemoveCertBinding(context.Background(), payload)
	var nf *user.CertBindingNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %T(%v), want *user.CertBindingNotFoundError", err, err)
	}
}

// TestHandleAddCertBinding_HijackAttemptIsAlreadyExists pins both halves for
// this transport: the handler keeps the store's typed refusal, and the status
// ladder renders it AlreadyExists rather than StatusError (go-qrdb#536).
func TestHandleAddCertBinding_HijackAttemptIsAlreadyExists(t *testing.T) {
	svc := &mockUserService{
		addCertBindingFn: func(_ context.Context, userID uint64, tp string, _ int64) (*user.User, error) {
			return nil, &user.CertBindingConflictError{Thumbprint: tp, BoundUserID: 7, TargetUserID: userID}
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithUserService(svc))

	payload, err := json.Marshal(addCertBindingWireRequest{UserID: 1001, Thumbprint: "AAAA"})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	_, err = srv.handleAddCertBinding(context.Background(), payload)
	var conflict *user.CertBindingConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %T(%v), want *user.CertBindingConflictError", err, err)
	}
	if st, msg := mapServiceError(err); st != wire.StatusAlreadyExists || !strings.Contains(string(msg), "already bound to user 7") {
		t.Fatalf("status = %s, msg = %q; want AlreadyExists naming user 7", wire.StatusName(st), msg)
	}
}

// TestUserVerbs_BootstrapAdminLockoutIsFailedPrecondition (go-qrdb#517): each
// user verb that can lock the bootstrap admin out keeps the store's typed
// refusal, and the status ladder renders it FailedPrecondition, as gRPC does.
func TestUserVerbs_BootstrapAdminLockoutIsFailedPrecondition(t *testing.T) {
	lockout := func(reason user.AdminLockoutReason) error {
		return &user.BootstrapAdminLockoutError{UserID: 1001, Reason: reason}
	}
	svc := &mockUserService{
		removeCertBindingFn: func(context.Context, uint64, string) (*user.User, error) {
			return nil, lockout(user.AdminLockoutNoLiveBinding)
		},
		deleteUserFn:  func(context.Context, uint64) error { return lockout(user.AdminLockoutDelete) },
		disableUserFn: func(context.Context, uint64) error { return lockout(user.AdminLockoutLifecycle) },
		updateUserFn: func(context.Context, *user.UpdateUserRequest) (*user.User, error) {
			return nil, lockout(user.AdminLockoutRename)
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithUserService(svc))
	cases := map[string]struct {
		handle  func(context.Context, []byte) ([]byte, error)
		request any
	}{
		"withdraw the last binding": {srv.handleRemoveCertBinding, removeCertBindingWireRequest{UserID: 1001, Thumbprint: "AAAA"}},
		"delete the admin":          {srv.handleDeleteUser, deleteUserWireRequest{ID: 1001}},
		"disable the admin":         {srv.handleDisableUser, disableUserWireRequest{ID: 1001}},
		"rename the admin":          {srv.handleUpdateUser, updateUserWireRequest{ID: 1001, Username: "root"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			payload, err := json.Marshal(tc.request)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			_, err = tc.handle(context.Background(), payload)
			var refused *user.BootstrapAdminLockoutError
			if !errors.As(err, &refused) {
				t.Fatalf("err = %T(%v), want *user.BootstrapAdminLockoutError", err, err)
			}
			if got, _ := mapServiceError(err); got != wire.StatusFailedPrecondition {
				t.Fatalf("status = %s, want FailedPrecondition", wire.StatusName(got))
			}
		})
	}
}

// TestAddCertBinding_PastExpiryIsInvalidArgument (go-qrdb#517): an enrolment
// whose expiry has already passed keeps its typed refusal and is
// InvalidArgument, as gRPC.
func TestAddCertBinding_PastExpiryIsInvalidArgument(t *testing.T) {
	svc := &mockUserService{
		addCertBindingFn: func(_ context.Context, _ uint64, tp string, notAfter int64) (*user.User, error) {
			return nil, &user.CertBindingExpiredError{Thumbprint: tp, NotAfter: time.Unix(notAfter, 0).UTC()}
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithUserService(svc))
	payload, err := json.Marshal(addCertBindingWireRequest{UserID: 1001, Thumbprint: "AAAA", NotAfterUnix: 1})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	_, err = srv.handleAddCertBinding(context.Background(), payload)
	var expired *user.CertBindingExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("err = %T(%v), want *user.CertBindingExpiredError", err, err)
	}
	if got, _ := mapServiceError(err); got != wire.StatusInvalidArgument {
		t.Fatalf("status = %s, want InvalidArgument", wire.StatusName(got))
	}
}

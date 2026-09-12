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
	"testing"

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

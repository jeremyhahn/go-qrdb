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
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mockUserAuthFactorSvc is a UserAuthFactorServicer whose every method
// succeeds and returns zero values: the double for handler paths that
// fail before, or never depend on, what the service returns.
type mockUserAuthFactorSvc struct{}

var _ service.UserAuthFactorServicer = (*mockUserAuthFactorSvc)(nil)

func (m *mockUserAuthFactorSvc) Add(_ context.Context, _ uint64, _ *auth.AuthFactor) error {
	return nil
}
func (m *mockUserAuthFactorSvc) List(_ context.Context, _ uint64) ([]*auth.AuthFactor, error) {
	return nil, nil
}
func (m *mockUserAuthFactorSvc) Get(_ context.Context, _ string) (*auth.AuthFactor, error) {
	return nil, nil
}
func (m *mockUserAuthFactorSvc) LookupByCredentialKey(_ context.Context, _ auth.AuthFactorType, _ []byte) (*auth.AuthFactor, error) {
	return nil, nil
}
func (m *mockUserAuthFactorSvc) Remove(_ context.Context, _ string, _ bool) error { return nil }
func (m *mockUserAuthFactorSvc) Delete(_ context.Context, _ string, _ bool) error { return nil }
func (m *mockUserAuthFactorSvc) Rotate(_ context.Context, _ string, _ *auth.AuthFactor) error {
	return nil
}
func (m *mockUserAuthFactorSvc) Relabel(_ context.Context, _ string, _ string) error     { return nil }
func (m *mockUserAuthFactorSvc) MarkUsed(_ context.Context, _ string, _ time.Time) error { return nil }

func (m *mockUserAuthFactorSvc) AddConditional(_ context.Context, _ uint64, _ *auth.AuthFactor, _ auth.FactorAddCondition) (uint64, error) {
	return 0, nil
}

func (m *mockUserAuthFactorSvc) FactorSetVersion(_ context.Context, _ uint64) (uint64, error) {
	return 0, nil
}

func TestHandleUserAuthFactorAdd_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(quicUserFactorAddRequest{UserID: 1, Type: "totp", Credential: []byte("cred")})
	_, err := srv.handleUserAuthFactorAdd(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleUserAuthFactorAdd_UnmarshalError(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithUserAuthFactorService(&mockUserAuthFactorSvc{}))
	_, err := srv.handleUserAuthFactorAdd(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T", err)
	}
}

func TestHandleUserAuthFactorList_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(quicUserFactorListRequest{UserID: 1})
	_, err := srv.handleUserAuthFactorList(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleUserAuthFactorGet_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(quicUserFactorGetRequest{FactorID: "f1"})
	_, err := srv.handleUserAuthFactorGet(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

// decodeFactorFrame decodes a {"<field>": record} response frame.
func decodeFactorFrame(t *testing.T, resp []byte, field string) quicUserFactorRecord {
	t.Helper()
	var out map[string]quicUserFactorRecord
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("decode %s frame %s: %v", field, resp, err)
	}
	rec, ok := out[field]
	if !ok {
		t.Fatalf("response %s has no %q", resp, field)
	}
	return rec
}

func TestHandleUserAuthFactorGet_Success(t *testing.T) {
	srv := &Server{userAuthFactorSvc: realUserFactorService(t)}
	id, _, err := addFrame(t, srv, quicUserFactorAddRequest{UserID: 42, Type: "mtls", Label: "laptop", Credential: []byte("cred")})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	payload, _ := json.Marshal(quicUserFactorGetRequest{FactorID: id})
	resp, err := srv.handleUserAuthFactorGet(context.Background(), payload)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got := decodeFactorFrame(t, resp, "factor")
	if got.ID != id || got.UserID != 42 || got.Type != "mtls" || got.Label != "laptop" {
		t.Errorf("factor = %+v, want id %q, user 42, mtls, label laptop", got, id)
	}
}

func TestHandleUserAuthFactorRemove_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(quicUserFactorRemoveRequest{FactorID: "f1"})
	_, err := srv.handleUserAuthFactorRemove(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleUserAuthFactorRemove_Success(t *testing.T) {
	srv := &Server{userAuthFactorSvc: realUserFactorService(t)}
	// Two factors, so removing one does not strand the user without a factor.
	id, _, err := addFrame(t, srv, quicUserFactorAddRequest{UserID: 42, Type: "mtls", Credential: []byte("cred-1")})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, _, err := addFrame(t, srv, quicUserFactorAddRequest{UserID: 42, Type: "fido2", Credential: []byte("cred-2")}); err != nil {
		t.Fatalf("Add second: %v", err)
	}
	payload, _ := json.Marshal(quicUserFactorRemoveRequest{FactorID: id})
	resp, err := srv.handleUserAuthFactorRemove(context.Background(), payload)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	var out map[string]string
	if err := json.Unmarshal(resp, &out); err != nil || out["status"] != "revoked" {
		t.Fatalf("response = %s (%v), want status revoked", resp, err)
	}
	getPayload, _ := json.Marshal(quicUserFactorGetRequest{FactorID: id})
	getResp, err := srv.handleUserAuthFactorGet(context.Background(), getPayload)
	if err != nil {
		t.Fatalf("Get after Remove: %v", err)
	}
	if got := decodeFactorFrame(t, getResp, "factor"); got.RevokedAt == "" {
		t.Errorf("factor %q after Remove = %+v, want revoked_at set", id, got)
	}
}

func TestHandleUserAuthFactorRotate_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(quicUserFactorRotateRequest{OldFactorID: "old"})
	_, err := srv.handleUserAuthFactorRotate(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleUserAuthFactorRotate_Success(t *testing.T) {
	srv := &Server{userAuthFactorSvc: realUserFactorService(t)}
	oldID, _, err := addFrame(t, srv, quicUserFactorAddRequest{UserID: 42, Type: "mtls", Credential: []byte("cred-1")})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	payload, _ := json.Marshal(quicUserFactorRotateRequest{
		OldFactorID: oldID,
		NewFactor:   quicUserFactorAddRequest{UserID: 42, Type: "mtls", Label: "rotated", Credential: []byte("cred-2")},
	})
	resp, err := srv.handleUserAuthFactorRotate(context.Background(), payload)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	got := decodeFactorFrame(t, resp, "new_factor")
	if got.ID == "" || got.ID == oldID || got.UserID != 42 || got.Label != "rotated" || string(got.Credential) != "cred-2" {
		t.Errorf("new_factor = %+v, want a fresh id for user 42 labelled rotated with cred-2", got)
	}
}

func TestHandleUserAuthFactorRelabel_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(quicUserFactorRelabelRequest{FactorID: "f1", Label: "new-label"})
	_, err := srv.handleUserAuthFactorRelabel(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleUserAuthFactorRelabel_Success(t *testing.T) {
	srv := &Server{userAuthFactorSvc: realUserFactorService(t)}
	id, _, err := addFrame(t, srv, quicUserFactorAddRequest{UserID: 42, Type: "mtls", Label: "old-label", Credential: []byte("cred")})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	payload, _ := json.Marshal(quicUserFactorRelabelRequest{FactorID: id, Label: "new-label"})
	resp, err := srv.handleUserAuthFactorRelabel(context.Background(), payload)
	if err != nil {
		t.Fatalf("Relabel: %v", err)
	}
	if got := decodeFactorFrame(t, resp, "factor"); got.ID != id || got.Label != "new-label" {
		t.Errorf("factor = %+v, want id %q relabelled new-label", got, id)
	}
}

func TestHandleUserAuthFactorLogin_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(quicUserFactorLoginRequest{Type: "totp", LookupKey: []byte("key")})
	_, err := srv.handleUserAuthFactorLogin(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleUserAuthFactorLogin_ServiceError(t *testing.T) {
	// Test the service-error path via a custom service that returns an error.
	svcErr := errors.New("lookup failed")
	svc := &loginErrUserAuthFactorSvc{err: svcErr}
	srv := NewServer(nil, nil, "localhost:0", WithUserAuthFactorService(svc))
	payload, _ := json.Marshal(quicUserFactorLoginRequest{Type: "mtls", LookupKey: []byte("k")})
	_, err := srv.handleUserAuthFactorLogin(context.Background(), payload)
	if !errors.Is(err, svcErr) {
		t.Fatalf("expected svcErr, got %v", err)
	}
}

// loginErrUserAuthFactorSvc returns an error from LookupByCredentialKey.
type loginErrUserAuthFactorSvc struct {
	mockUserAuthFactorSvc
	err error
}

func (s *loginErrUserAuthFactorSvc) LookupByCredentialKey(_ context.Context, _ auth.AuthFactorType, _ []byte) (*auth.AuthFactor, error) {
	return nil, s.err
}

func TestUserFactorToQUIC_Nil(t *testing.T) {
	if got := userFactorToQUIC(nil); !reflect.DeepEqual(got, quicUserFactorRecord{}) {
		t.Errorf("userFactorToQUIC(nil) = %+v, want the zero record", got)
	}
}

func TestUserFactorToQUIC_WithLastUsedAt(t *testing.T) {
	registered := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	used := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	f := &auth.AuthFactor{
		ID:             "f1",
		UserID:         42,
		Type:           auth.AuthFactorMTLS,
		Label:          "My mTLS",
		Credential:     []byte("secret"),
		SPKICredential: []byte("spki"),
		Metadata:       map[string]string{"k": "v"},
		RegisteredAt:   registered,
		LastUsedAt:     &used,
		CreatedBy:      "admin",
	}
	want := quicUserFactorRecord{
		ID:             "f1",
		UserID:         42,
		Type:           string(auth.AuthFactorMTLS),
		Label:          "My mTLS",
		Credential:     []byte("secret"),
		SPKICredential: []byte("spki"),
		Metadata:       map[string]string{"k": "v"},
		RegisteredAt:   "2026-01-01T00:00:00Z",
		LastUsedAt:     "2026-01-02T03:04:05Z",
		CreatedBy:      "admin",
	}
	if got := userFactorToQUIC(f); !reflect.DeepEqual(got, want) {
		t.Errorf("userFactorToQUIC = %+v, want %+v", got, want)
	}
}

func TestUserFactorToQUIC_WithRevokedAt(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := &auth.AuthFactor{
		ID:           "f2",
		RegisteredAt: now,
		RevokedAt:    &now,
	}
	got := userFactorToQUIC(f)
	if got.RevokedAt != "2026-01-01T00:00:00Z" || got.LastUsedAt != "" {
		t.Errorf("revoked_at = %q, last_used_at = %q; want RFC 3339 revoked_at and no last_used_at", got.RevokedAt, got.LastUsedAt)
	}
}

func TestUserFactorFromQUIC_Fields(t *testing.T) {
	req := &quicUserFactorAddRequest{
		UserID:         10,
		Type:           "totp",
		Label:          "test",
		Credential:     []byte("secret"),
		SPKICredential: []byte("spki"),
		Metadata:       map[string]string{"k": "v"},
		CreatedBy:      "admin",
	}
	want := &auth.AuthFactor{
		UserID:         10,
		Type:           auth.AuthFactorType("totp"),
		Label:          "test",
		Credential:     []byte("secret"),
		SPKICredential: []byte("spki"),
		Metadata:       map[string]string{"k": "v"},
		CreatedBy:      "admin",
	}
	if got := userFactorFromQUIC(req); !reflect.DeepEqual(got, want) {
		t.Errorf("userFactorFromQUIC = %+v, want %+v", got, want)
	}
}

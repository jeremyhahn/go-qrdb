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
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
)

// credentialTestError is a typed error used to verify error propagation
// from the service layer through the QUIC handlers.
type credentialTestError struct {
	msg string
}

func (e *credentialTestError) Error() string { return e.msg }

// newCredentialTestServer creates a Server wired with the supplied
// credential service mock and no other dependencies. The kv/admin
// services are nil; their handlers are not exercised by these tests.
func newCredentialTestServer(t *testing.T, svc *mockCredentialService) *Server {
	t.Helper()
	return NewServer(nil, nil, "localhost:0", WithCredentialService(svc))
}

// ---------------------------------------------------------------------------
// handleCredentialSubmit
// ---------------------------------------------------------------------------

func TestHandleCredentialSubmit_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.CredentialSubmitRequest{Name: "db", Value: []byte("secret")})

	_, err := srv.handleCredentialSubmit(context.Background(), payload)
	if err == nil {
		t.Fatal("expected ServiceNotConfiguredError, got nil")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleCredentialSubmit_Success(t *testing.T) {
	called := false
	mock := &mockCredentialService{
		submitFn: func(_ context.Context, name string, value []byte) error {
			called = true
			if name != "db" {
				t.Errorf("name = %q, want db", name)
			}
			if string(value) != "secret" {
				t.Errorf("value = %q, want secret", string(value))
			}
			return nil
		},
	}
	srv := newCredentialTestServer(t, mock)

	payload, err := proto.Marshal(&pb.CredentialSubmitRequest{Name: "db", Value: []byte("secret")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := srv.handleCredentialSubmit(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Fatalf("expected nil response, got %q", resp)
	}
	if !called {
		t.Fatal("expected CredentialSubmit to be called")
	}
}

func TestHandleCredentialSubmit_ServiceError(t *testing.T) {
	svcErr := &credentialTestError{msg: "submit failed"}
	mock := &mockCredentialService{
		submitFn: func(_ context.Context, _ string, _ []byte) error { return svcErr },
	}
	srv := newCredentialTestServer(t, mock)
	payload, _ := proto.Marshal(&pb.CredentialSubmitRequest{Name: "k", Value: []byte("v")})

	_, err := srv.handleCredentialSubmit(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var te *credentialTestError
	if !errors.As(err, &te) {
		t.Fatalf("expected *credentialTestError, got %T", err)
	}
}

func TestHandleCredentialSubmit_UnmarshalError(t *testing.T) {
	srv := newCredentialTestServer(t, &mockCredentialService{})
	_, err := srv.handleCredentialSubmit(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleCredentialStrategy
// ---------------------------------------------------------------------------

func TestHandleCredentialStrategy_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	_, err := srv.handleCredentialStrategy(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleCredentialStrategy_Success(t *testing.T) {
	mock := &mockCredentialService{
		strategyFn: func() string { return "barrier" },
	}
	srv := newCredentialTestServer(t, mock)
	resp, err := srv.handleCredentialStrategy(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var pbResp pb.CredentialStrategyResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if pbResp.GetStrategy() != "barrier" {
		t.Fatalf("strategy = %q, want barrier", pbResp.GetStrategy())
	}
}

// ---------------------------------------------------------------------------
// handleCredentialDelete
// ---------------------------------------------------------------------------

func TestHandleCredentialDelete_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.CredentialDeleteRequest{Name: "k"})
	_, err := srv.handleCredentialDelete(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleCredentialDelete_Success(t *testing.T) {
	called := ""
	mock := &mockCredentialService{
		deleteFn: func(_ context.Context, name string) error {
			called = name
			return nil
		},
	}
	srv := newCredentialTestServer(t, mock)
	payload, _ := proto.Marshal(&pb.CredentialDeleteRequest{Name: "db"})
	resp, err := srv.handleCredentialDelete(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != nil {
		t.Fatal("expected nil response")
	}
	if called != "db" {
		t.Fatalf("Delete called with %q, want db", called)
	}
}

func TestHandleCredentialDelete_ServiceError(t *testing.T) {
	svcErr := &credentialTestError{msg: "delete failed"}
	mock := &mockCredentialService{
		deleteFn: func(_ context.Context, _ string) error { return svcErr },
	}
	srv := newCredentialTestServer(t, mock)
	payload, _ := proto.Marshal(&pb.CredentialDeleteRequest{Name: "k"})
	_, err := srv.handleCredentialDelete(context.Background(), payload)
	var te *credentialTestError
	if !errors.As(err, &te) {
		t.Fatalf("expected *credentialTestError, got %T", err)
	}
}

func TestHandleCredentialDelete_UnmarshalError(t *testing.T) {
	srv := newCredentialTestServer(t, &mockCredentialService{})
	_, err := srv.handleCredentialDelete(context.Background(), []byte{0xff})
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleCredentialList
// ---------------------------------------------------------------------------

func TestHandleCredentialList_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	_, err := srv.handleCredentialList(context.Background(), nil)
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleCredentialList_Success(t *testing.T) {
	mock := &mockCredentialService{
		listFn: func(_ context.Context) ([]string, error) {
			return []string{"alpha", "bravo", "charlie"}, nil
		},
	}
	srv := newCredentialTestServer(t, mock)
	payload, _ := proto.Marshal(&pb.CredentialListRequest{})
	resp, err := srv.handleCredentialList(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var pbResp pb.CredentialListResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := pbResp.GetNames()
	if len(got) != 3 || got[0] != "alpha" || got[1] != "bravo" || got[2] != "charlie" {
		t.Fatalf("names = %v, want [alpha bravo charlie]", got)
	}
}

func TestHandleCredentialList_ServiceError(t *testing.T) {
	svcErr := &credentialTestError{msg: "list failed"}
	mock := &mockCredentialService{
		listFn: func(_ context.Context) ([]string, error) { return nil, svcErr },
	}
	srv := newCredentialTestServer(t, mock)
	_, err := srv.handleCredentialList(context.Background(), nil)
	var te *credentialTestError
	if !errors.As(err, &te) {
		t.Fatalf("expected *credentialTestError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleCredentialRotate
// ---------------------------------------------------------------------------

func TestHandleCredentialRotate_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.CredentialRotateRequest{Name: "k", NewValue: []byte("v")})
	_, err := srv.handleCredentialRotate(context.Background(), payload)
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleCredentialRotate_SuccessWithGrace(t *testing.T) {
	graceUntil := time.Unix(1_700_000_000, 0).UTC()
	var capturedOpts credentials.RotateOptions
	mock := &mockCredentialService{
		rotateFn: func(_ context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error) {
			capturedOpts = opts
			return &credentials.RotateResult{
				Name:            name,
				PreviousVersion: 3,
				NewVersion:      4,
				GraceUntil:      graceUntil,
			}, nil
		},
	}
	srv := newCredentialTestServer(t, mock)

	payload, _ := proto.Marshal(&pb.CredentialRotateRequest{
		Name:               "db",
		NewValue:           []byte("secret-v2"),
		GracePeriodSeconds: 60,
	})
	resp, err := srv.handleCredentialRotate(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var pbResp pb.CredentialRotateResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if pbResp.GetName() != "db" || pbResp.GetPreviousVersion() != 3 || pbResp.GetNewVersion() != 4 {
		t.Fatalf("response = %+v, want name=db prev=3 new=4", &pbResp)
	}
	if pbResp.GetGraceUntilUnix() != graceUntil.Unix() {
		t.Fatalf("GraceUntilUnix = %d, want %d", pbResp.GetGraceUntilUnix(), graceUntil.Unix())
	}
	if string(capturedOpts.NewValue) != "secret-v2" {
		t.Fatalf("NewValue captured = %q, want secret-v2", string(capturedOpts.NewValue))
	}
	if capturedOpts.GracePeriod != 60*time.Second {
		t.Fatalf("GracePeriod captured = %v, want 60s", capturedOpts.GracePeriod)
	}
}

func TestHandleCredentialRotate_ZeroGraceOmitsGraceUntil(t *testing.T) {
	mock := &mockCredentialService{
		rotateFn: func(_ context.Context, name string, _ credentials.RotateOptions) (*credentials.RotateResult, error) {
			return &credentials.RotateResult{
				Name:            name,
				PreviousVersion: 0,
				NewVersion:      1,
				// GraceUntil is zero-value.
			}, nil
		},
	}
	srv := newCredentialTestServer(t, mock)
	payload, _ := proto.Marshal(&pb.CredentialRotateRequest{Name: "k", NewValue: []byte("v")})
	resp, err := srv.handleCredentialRotate(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var pbResp pb.CredentialRotateResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if pbResp.GetGraceUntilUnix() != 0 {
		t.Fatalf("GraceUntilUnix = %d, want 0", pbResp.GetGraceUntilUnix())
	}
}

func TestHandleCredentialRotate_ServiceError(t *testing.T) {
	svcErr := &credentialTestError{msg: "rotate failed"}
	mock := &mockCredentialService{
		rotateFn: func(_ context.Context, _ string, _ credentials.RotateOptions) (*credentials.RotateResult, error) {
			return nil, svcErr
		},
	}
	srv := newCredentialTestServer(t, mock)
	payload, _ := proto.Marshal(&pb.CredentialRotateRequest{Name: "k", NewValue: []byte("v")})
	_, err := srv.handleCredentialRotate(context.Background(), payload)
	var te *credentialTestError
	if !errors.As(err, &te) {
		t.Fatalf("expected *credentialTestError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleCredentialRetrieveVersion
// ---------------------------------------------------------------------------

func TestHandleCredentialRetrieveVersion_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.CredentialRetrieveVersionRequest{Name: "k", Version: 1})
	_, err := srv.handleCredentialRetrieveVersion(context.Background(), payload)
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleCredentialRetrieveVersion_Success(t *testing.T) {
	mock := &mockCredentialService{
		retrieveVersionFn: func(_ context.Context, name string, version uint64) ([]byte, error) {
			if name != "db" || version != 2 {
				t.Errorf("unexpected args: name=%q version=%d", name, version)
			}
			return []byte("plaintext-secret"), nil
		},
	}
	srv := newCredentialTestServer(t, mock)
	payload, _ := proto.Marshal(&pb.CredentialRetrieveVersionRequest{Name: "db", Version: 2})
	resp, err := srv.handleCredentialRetrieveVersion(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var pbResp pb.CredentialRetrieveVersionResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(pbResp.GetValue()) != "plaintext-secret" {
		t.Fatalf("value = %q, want plaintext-secret", pbResp.GetValue())
	}
}

func TestHandleCredentialRetrieveVersion_ServiceError(t *testing.T) {
	svcErr := &credentialTestError{msg: "retrieve failed"}
	mock := &mockCredentialService{
		retrieveVersionFn: func(_ context.Context, _ string, _ uint64) ([]byte, error) { return nil, svcErr },
	}
	srv := newCredentialTestServer(t, mock)
	payload, _ := proto.Marshal(&pb.CredentialRetrieveVersionRequest{Name: "k", Version: 1})
	_, err := srv.handleCredentialRetrieveVersion(context.Background(), payload)
	var te *credentialTestError
	if !errors.As(err, &te) {
		t.Fatalf("expected *credentialTestError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleCredentialListVersions
// ---------------------------------------------------------------------------

func TestHandleCredentialListVersions_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.CredentialListVersionsRequest{Name: "k"})
	_, err := srv.handleCredentialListVersions(context.Background(), payload)
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleCredentialListVersions_Success(t *testing.T) {
	created := time.Unix(1_700_000_000, 0).UTC()
	grace := time.Unix(1_700_003_600, 0).UTC()
	mock := &mockCredentialService{
		listVersionsFn: func(_ context.Context, _ string) ([]credentials.CredentialVersion, error) {
			return []credentials.CredentialVersion{
				{Version: 2, IsCurrent: true, CreatedAt: created},
				{Version: 1, IsCurrent: false, CreatedAt: created, GraceUntil: grace},
			}, nil
		},
	}
	srv := newCredentialTestServer(t, mock)
	payload, _ := proto.Marshal(&pb.CredentialListVersionsRequest{Name: "db"})
	resp, err := srv.handleCredentialListVersions(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var pbResp pb.CredentialListVersionsResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if pbResp.GetName() != "db" {
		t.Fatalf("name = %q, want db", pbResp.GetName())
	}
	versions := pbResp.GetVersions()
	if len(versions) != 2 {
		t.Fatalf("len(versions) = %d, want 2", len(versions))
	}
	if versions[0].GetVersion() != 2 || !versions[0].GetIsCurrent() {
		t.Fatalf("v0 = %+v, want version=2 current=true", versions[0])
	}
	if versions[0].GetCreatedAtUnix() != created.Unix() {
		t.Fatalf("v0 CreatedAtUnix = %d, want %d", versions[0].GetCreatedAtUnix(), created.Unix())
	}
	if versions[0].GetGraceUntilUnix() != 0 {
		t.Fatalf("v0 GraceUntilUnix = %d, want 0", versions[0].GetGraceUntilUnix())
	}
	if versions[1].GetVersion() != 1 || versions[1].GetIsCurrent() {
		t.Fatalf("v1 = %+v, want version=1 current=false", versions[1])
	}
	if versions[1].GetGraceUntilUnix() != grace.Unix() {
		t.Fatalf("v1 GraceUntilUnix = %d, want %d", versions[1].GetGraceUntilUnix(), grace.Unix())
	}
}

func TestHandleCredentialListVersions_ServiceError(t *testing.T) {
	svcErr := &credentialTestError{msg: "list versions failed"}
	mock := &mockCredentialService{
		listVersionsFn: func(_ context.Context, _ string) ([]credentials.CredentialVersion, error) {
			return nil, svcErr
		},
	}
	srv := newCredentialTestServer(t, mock)
	payload, _ := proto.Marshal(&pb.CredentialListVersionsRequest{Name: "k"})
	_, err := srv.handleCredentialListVersions(context.Background(), payload)
	var te *credentialTestError
	if !errors.As(err, &te) {
		t.Fatalf("expected *credentialTestError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleCredentialGCExpired
// ---------------------------------------------------------------------------

func TestHandleCredentialGCExpired_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.CredentialGCRequest{Name: "k"})
	_, err := srv.handleCredentialGCExpired(context.Background(), payload)
	var sncErr *ServiceNotConfiguredError
	if !errors.As(err, &sncErr) {
		t.Fatalf("expected *ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleCredentialGCExpired_Success(t *testing.T) {
	mock := &mockCredentialService{
		gcExpiredFn: func(_ context.Context, name string) (int, error) {
			if name != "db" {
				t.Errorf("name = %q, want db", name)
			}
			return 7, nil
		},
	}
	srv := newCredentialTestServer(t, mock)
	payload, _ := proto.Marshal(&pb.CredentialGCRequest{Name: "db"})
	resp, err := srv.handleCredentialGCExpired(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var pbResp pb.CredentialGCResponse
	if err := proto.Unmarshal(resp, &pbResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if pbResp.GetReaped() != 7 {
		t.Fatalf("Reaped = %d, want 7", pbResp.GetReaped())
	}
}

func TestHandleCredentialGCExpired_ServiceError(t *testing.T) {
	svcErr := &credentialTestError{msg: "gc failed"}
	mock := &mockCredentialService{
		gcExpiredFn: func(_ context.Context, _ string) (int, error) { return 0, svcErr },
	}
	srv := newCredentialTestServer(t, mock)
	payload, _ := proto.Marshal(&pb.CredentialGCRequest{Name: "k"})
	_, err := srv.handleCredentialGCExpired(context.Background(), payload)
	var te *credentialTestError
	if !errors.As(err, &te) {
		t.Fatalf("expected *credentialTestError, got %T", err)
	}
}

func TestHandleCredentialStore_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.CredentialStoreRequest{Name: "k", Value: []byte("v")})
	_, err := srv.handleCredentialStore(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) || snc.Service != "credential" {
		t.Fatalf("expected ServiceNotConfiguredError{credential}, got %T(%v)", err, err)
	}
}

func TestHandleCredentialStore_UnmarshalError(t *testing.T) {
	srv := newCredentialTestServer(t, &mockCredentialService{})
	_, err := srv.handleCredentialStore(context.Background(), []byte{0xff, 0xfe})
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleCredentialStore_Success(t *testing.T) {
	var gotName, gotValue string
	mock := &mockCredentialService{
		storeFn: func(_ context.Context, name string, value []byte) error {
			gotName, gotValue = name, string(value)
			return nil
		},
	}
	srv := newCredentialTestServer(t, mock)
	payload, _ := proto.Marshal(&pb.CredentialStoreRequest{Name: "db-pass", Value: []byte("s3cret")})
	resp, err := srv.handleCredentialStore(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleCredentialStore: %v", err)
	}
	if resp != nil {
		t.Fatalf("expected nil response, got %q", resp)
	}
	if gotName != "db-pass" || gotValue != "s3cret" {
		t.Fatalf("service got (%q, %q), want (db-pass, s3cret)", gotName, gotValue)
	}
}

func TestHandleCredentialStore_ServiceError(t *testing.T) {
	svcErr := &credentialTestError{msg: "store failed"}
	mock := &mockCredentialService{
		storeFn: func(context.Context, string, []byte) error { return svcErr },
	}
	srv := newCredentialTestServer(t, mock)
	payload, _ := proto.Marshal(&pb.CredentialStoreRequest{Name: "k", Value: []byte("v")})
	if _, err := srv.handleCredentialStore(context.Background(), payload); !errors.Is(err, svcErr) {
		t.Fatalf("expected the service error, got %v", err)
	}
}

func TestHandleCredentialRetrieve_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.CredentialRetrieveRequest{Name: "k"})
	_, err := srv.handleCredentialRetrieve(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) || snc.Service != "credential" {
		t.Fatalf("expected ServiceNotConfiguredError{credential}, got %T(%v)", err, err)
	}
}

func TestHandleCredentialRetrieve_UnmarshalError(t *testing.T) {
	srv := newCredentialTestServer(t, &mockCredentialService{})
	_, err := srv.handleCredentialRetrieve(context.Background(), []byte{0xff, 0xfe})
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

func TestHandleCredentialRetrieve_Success(t *testing.T) {
	var gotName string
	mock := &mockCredentialService{
		retrieveFn: func(_ context.Context, name string) ([]byte, error) {
			gotName = name
			return []byte("s3cret"), nil
		},
	}
	srv := newCredentialTestServer(t, mock)
	payload, _ := proto.Marshal(&pb.CredentialRetrieveRequest{Name: "db-pass"})
	resp, err := srv.handleCredentialRetrieve(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleCredentialRetrieve: %v", err)
	}
	if gotName != "db-pass" {
		t.Fatalf("service got name %q, want db-pass", gotName)
	}
	var out pb.CredentialRetrieveResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(out.GetValue()) != "s3cret" {
		t.Fatalf("value = %q, want s3cret", out.GetValue())
	}
}

func TestHandleCredentialRetrieve_ServiceError(t *testing.T) {
	svcErr := &credentialTestError{msg: "retrieve failed"}
	mock := &mockCredentialService{
		retrieveFn: func(context.Context, string) ([]byte, error) { return nil, svcErr },
	}
	srv := newCredentialTestServer(t, mock)
	payload, _ := proto.Marshal(&pb.CredentialRetrieveRequest{Name: "k"})
	if _, err := srv.handleCredentialRetrieve(context.Background(), payload); !errors.Is(err, svcErr) {
		t.Fatalf("expected the service error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Dispatch registration
// ---------------------------------------------------------------------------

// TestBuildHandlers_RegistersCredentialHandlersWhenServiceProvided ensures
// that all eight credential methods become discoverable on the server when
// a CredentialServicer is wired in via WithCredentialService.
func TestBuildHandlers_RegistersCredentialHandlersWhenServiceProvided(t *testing.T) {
	srv := newCredentialTestServer(t, &mockCredentialService{})

	expected := []string{
		"credential.Submit",
		"credential.Strategy",
		"credential.Delete",
		"credential.List",
		"credential.Rotate",
		"credential.RetrieveVersion",
		"credential.ListVersions",
		"credential.GCExpired",
	}
	for _, m := range expected {
		if _, ok := srv.handlers[m]; !ok {
			t.Errorf("method %q not registered", m)
		}
	}
}

// TestBuildHandlers_OmitsCredentialHandlersWhenServiceAbsent ensures that
// no credential methods are registered when the service is not configured
// (matching the conditional-registration pattern used elsewhere).
func TestBuildHandlers_OmitsCredentialHandlersWhenServiceAbsent(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	for m := range srv.handlers {
		if len(m) >= 11 && m[:11] == "credential." {
			t.Errorf("unexpected credential handler registered: %q", m)
		}
	}
}

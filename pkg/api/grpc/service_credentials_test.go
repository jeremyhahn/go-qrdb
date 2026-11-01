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
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// ---------------------------------------------------------------------------
// fakeCredentialServicerProto is an in-memory service.CredentialServicer used to
// exercise the gRPC handlers without spinning up a barrier or storage layer.
// ---------------------------------------------------------------------------

type fakeCredentialServicerProto struct {
	storeFn        func(ctx context.Context, name string, value []byte) error
	retrieveFn     func(ctx context.Context, name string) ([]byte, error)
	submitFn       func(ctx context.Context, name string, value []byte) error
	deleteFn       func(ctx context.Context, name string) error
	strategyFn     func() string
	listFn         func(ctx context.Context) ([]string, error)
	rotateFn       func(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error)
	retrieveVersFn func(ctx context.Context, name string, version uint64) ([]byte, error)
	listVersionsFn func(ctx context.Context, name string) ([]credentials.CredentialVersion, error)
	gcExpiredFn    func(ctx context.Context, name string) (int, error)
}

func (f *fakeCredentialServicerProto) CredentialStore(ctx context.Context, name string, value []byte) error {
	if f.storeFn != nil {
		return f.storeFn(ctx, name, value)
	}
	return nil
}

func (f *fakeCredentialServicerProto) retrieveValue(ctx context.Context, name string) ([]byte, error) {
	if f.retrieveFn != nil {
		return f.retrieveFn(ctx, name)
	}
	return nil, nil
}

func (f *fakeCredentialServicerProto) CredentialSubmit(ctx context.Context, name string, value []byte) error {
	if f.submitFn != nil {
		return f.submitFn(ctx, name, value)
	}
	return nil
}

func (f *fakeCredentialServicerProto) CredentialDelete(ctx context.Context, name string) error {
	if f.deleteFn != nil {
		return f.deleteFn(ctx, name)
	}
	return nil
}

func (f *fakeCredentialServicerProto) CredentialStrategy(context.Context) (string, error) {
	return f.credentialStrategy(), nil
}

func (f *fakeCredentialServicerProto) credentialStrategy() string {
	if f.strategyFn != nil {
		return f.strategyFn()
	}
	return "manual"
}

func (f *fakeCredentialServicerProto) CredentialList(ctx context.Context) ([]string, error) {
	if f.listFn != nil {
		return f.listFn(ctx)
	}
	return nil, nil
}

func (f *fakeCredentialServicerProto) CredentialRotate(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error) {
	if f.rotateFn != nil {
		return f.rotateFn(ctx, name, opts)
	}
	return nil, nil
}

func (f *fakeCredentialServicerProto) retrieveVersionValue(ctx context.Context, name string, version uint64) ([]byte, error) {
	if f.retrieveVersFn != nil {
		return f.retrieveVersFn(ctx, name, version)
	}
	return nil, nil
}

func (f *fakeCredentialServicerProto) CredentialRetrieveBuffer(ctx context.Context, name string) (*secure.Buffer, error) {
	v, err := f.retrieveValue(ctx, name)
	if err != nil {
		return nil, err
	}
	return secure.NewBufferFromBytes(append([]byte(nil), v...))
}

func (f *fakeCredentialServicerProto) CredentialRetrieveVersionBuffer(ctx context.Context, name string, version uint64) (*secure.Buffer, error) {
	v, err := f.retrieveVersionValue(ctx, name, version)
	if err != nil {
		return nil, err
	}
	return secure.NewBufferFromBytes(append([]byte(nil), v...))
}

func (f *fakeCredentialServicerProto) CredentialListVersions(ctx context.Context, name string) ([]credentials.CredentialVersion, error) {
	if f.listVersionsFn != nil {
		return f.listVersionsFn(ctx, name)
	}
	return nil, nil
}

func (f *fakeCredentialServicerProto) CredentialGCExpired(ctx context.Context, name string) (int, error) {
	if f.gcExpiredFn != nil {
		return f.gcExpiredFn(ctx, name)
	}
	return 0, nil
}

// codeOf returns the gRPC status code carried by err, or codes.Unknown when
// err is nil or not a status error.
func codeOf(err error) codes.Code {
	if err == nil {
		return codes.OK
	}
	if st, ok := status.FromError(err); ok {
		return st.Code()
	}
	return codes.Unknown
}

// ---------------------------------------------------------------------------
// CredentialSubmit
// ---------------------------------------------------------------------------

func TestCredentialSubmit_Success_Proto(t *testing.T) {
	var captured struct {
		name  string
		value []byte
	}
	svc := &fakeCredentialServicerProto{
		submitFn: func(_ context.Context, name string, value []byte) error {
			captured.name = name
			captured.value = value
			return nil
		},
	}
	s := &qrdbServiceServer{credentialSvc: svc}

	resp, err := s.CredentialSubmit(context.Background(), &pb.CredentialSubmitRequest{Name: "n", Value: []byte("v")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil empty response")
	}
	if captured.name != "n" || string(captured.value) != "v" {
		t.Errorf("unexpected captured args: %+v", captured)
	}
}

func TestCredentialSubmit_NilService_Proto(t *testing.T) {
	s := &qrdbServiceServer{}
	_, err := s.CredentialSubmit(context.Background(), &pb.CredentialSubmitRequest{Name: "n"})
	if codeOf(err) != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s (%v)", codeOf(err), err)
	}
}

func TestCredentialSubmit_ErrorMapping_Proto(t *testing.T) {
	svc := &fakeCredentialServicerProto{
		submitFn: func(_ context.Context, _ string, _ []byte) error {
			return credentials.ErrEmptyCredentialName
		},
	}
	s := &qrdbServiceServer{credentialSvc: svc}
	_, err := s.CredentialSubmit(context.Background(), &pb.CredentialSubmitRequest{Name: ""})
	if codeOf(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", codeOf(err))
	}
}

// ---------------------------------------------------------------------------
// CredentialStrategy
// ---------------------------------------------------------------------------

func TestCredentialStrategy_Success_Proto(t *testing.T) {
	svc := &fakeCredentialServicerProto{strategyFn: func() string { return "barrier" }}
	s := &qrdbServiceServer{credentialSvc: svc}

	resp, err := s.CredentialStrategy(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetStrategy() != "barrier" {
		t.Errorf("expected strategy 'barrier', got %q", resp.GetStrategy())
	}
}

func TestCredentialStrategy_NilService_Proto(t *testing.T) {
	s := &qrdbServiceServer{}
	_, err := s.CredentialStrategy(context.Background(), &emptypb.Empty{})
	if codeOf(err) != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", codeOf(err))
	}
}

// ---------------------------------------------------------------------------
// CredentialDelete
// ---------------------------------------------------------------------------

func TestCredentialDelete_Success_Proto(t *testing.T) {
	var capturedName string
	svc := &fakeCredentialServicerProto{
		deleteFn: func(_ context.Context, name string) error {
			capturedName = name
			return nil
		},
	}
	s := &qrdbServiceServer{credentialSvc: svc}

	if _, err := s.CredentialDelete(context.Background(), &pb.CredentialDeleteRequest{Name: "n"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedName != "n" {
		t.Errorf("unexpected captured name: %q", capturedName)
	}
}

func TestCredentialDelete_NilService_Proto(t *testing.T) {
	s := &qrdbServiceServer{}
	_, err := s.CredentialDelete(context.Background(), &pb.CredentialDeleteRequest{Name: "n"})
	if codeOf(err) != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", codeOf(err))
	}
}

func TestCredentialDelete_NotFound_Proto(t *testing.T) {
	svc := &fakeCredentialServicerProto{
		deleteFn: func(_ context.Context, _ string) error {
			return credentials.ErrCredentialNotFound
		},
	}
	s := &qrdbServiceServer{credentialSvc: svc}
	_, err := s.CredentialDelete(context.Background(), &pb.CredentialDeleteRequest{Name: "missing"})
	if codeOf(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", codeOf(err))
	}
}

// ---------------------------------------------------------------------------
// CredentialList
// ---------------------------------------------------------------------------

func TestCredentialList_Success_Proto(t *testing.T) {
	svc := &fakeCredentialServicerProto{
		listFn: func(_ context.Context) ([]string, error) {
			return []string{"a", "b", "c"}, nil
		},
	}
	s := &qrdbServiceServer{credentialSvc: svc}

	resp, err := s.CredentialList(context.Background(), &pb.CredentialListRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := resp.GetNames()
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("unexpected names: %v", got)
	}
}

func TestCredentialList_NilService_Proto(t *testing.T) {
	s := &qrdbServiceServer{}
	_, err := s.CredentialList(context.Background(), &pb.CredentialListRequest{})
	if codeOf(err) != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", codeOf(err))
	}
}

func TestCredentialList_EnumerationNotSupported_Proto(t *testing.T) {
	svc := &fakeCredentialServicerProto{
		listFn: func(_ context.Context) ([]string, error) {
			return nil, credentials.ErrEnumerationNotSupported
		},
	}
	s := &qrdbServiceServer{credentialSvc: svc}

	_, err := s.CredentialList(context.Background(), &pb.CredentialListRequest{})
	if codeOf(err) != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", codeOf(err))
	}
}

// ---------------------------------------------------------------------------
// CredentialRotate
// ---------------------------------------------------------------------------

func TestCredentialRotate_Success_Proto(t *testing.T) {
	graceUntil := time.Unix(1_700_000_000, 0).UTC()
	var capturedOpts credentials.RotateOptions
	svc := &fakeCredentialServicerProto{
		rotateFn: func(_ context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error) {
			capturedOpts = opts
			return &credentials.RotateResult{
				Name:            name,
				PreviousVersion: 2,
				NewVersion:      3,
				GraceUntil:      graceUntil,
			}, nil
		},
	}
	s := &qrdbServiceServer{credentialSvc: svc}

	resp, err := s.CredentialRotate(context.Background(), &pb.CredentialRotateRequest{
		Name:               "cred",
		NewValue:           []byte("new-secret"),
		GracePeriodSeconds: 60,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetName() != "cred" || resp.GetPreviousVersion() != 2 || resp.GetNewVersion() != 3 {
		t.Errorf("unexpected response: %+v", resp)
	}
	if resp.GetGraceUntilUnix() != graceUntil.Unix() {
		t.Errorf("expected grace_until_unix=%d, got %d", graceUntil.Unix(), resp.GetGraceUntilUnix())
	}
	if capturedOpts.GracePeriod != 60*time.Second {
		t.Errorf("expected GracePeriod=60s, got %v", capturedOpts.GracePeriod)
	}
	if string(capturedOpts.NewValue) != "new-secret" {
		t.Errorf("unexpected new value: %q", capturedOpts.NewValue)
	}
}

func TestCredentialRotate_ZeroGraceOmitsUnix_Proto(t *testing.T) {
	svc := &fakeCredentialServicerProto{
		rotateFn: func(_ context.Context, _ string, _ credentials.RotateOptions) (*credentials.RotateResult, error) {
			return &credentials.RotateResult{Name: "n", PreviousVersion: 1, NewVersion: 2}, nil
		},
	}
	s := &qrdbServiceServer{credentialSvc: svc}

	resp, err := s.CredentialRotate(context.Background(), &pb.CredentialRotateRequest{Name: "n", NewValue: []byte("v")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetGraceUntilUnix() != 0 {
		t.Errorf("expected zero grace_until_unix, got %d", resp.GetGraceUntilUnix())
	}
}

func TestCredentialRotate_NilService_Proto(t *testing.T) {
	s := &qrdbServiceServer{}
	_, err := s.CredentialRotate(context.Background(), &pb.CredentialRotateRequest{Name: "n"})
	if codeOf(err) != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", codeOf(err))
	}
}

func TestCredentialRotate_InvalidGracePeriod_Proto(t *testing.T) {
	svc := &fakeCredentialServicerProto{
		rotateFn: func(_ context.Context, _ string, _ credentials.RotateOptions) (*credentials.RotateResult, error) {
			return nil, credentials.ErrInvalidGracePeriod
		},
	}
	s := &qrdbServiceServer{credentialSvc: svc}

	_, err := s.CredentialRotate(context.Background(), &pb.CredentialRotateRequest{Name: "n", NewValue: []byte("v")})
	if codeOf(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", codeOf(err))
	}
}

// ---------------------------------------------------------------------------
// CredentialRetrieveVersion
// ---------------------------------------------------------------------------

func TestCredentialRetrieveVersion_Success_Proto(t *testing.T) {
	var capturedVersion uint64
	svc := &fakeCredentialServicerProto{
		retrieveVersFn: func(_ context.Context, _ string, version uint64) ([]byte, error) {
			capturedVersion = version
			return []byte("secret"), nil
		},
	}
	s := &qrdbServiceServer{credentialSvc: svc}

	resp, err := s.CredentialRetrieveVersion(context.Background(), &pb.CredentialRetrieveVersionRequest{
		Name: "n", Version: 7,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(resp.GetValue()) != "secret" {
		t.Errorf("unexpected value: %q", resp.GetValue())
	}
	if capturedVersion != 7 {
		t.Errorf("expected version=7, got %d", capturedVersion)
	}
}

func TestCredentialRetrieveVersion_NilService_Proto(t *testing.T) {
	s := &qrdbServiceServer{}
	_, err := s.CredentialRetrieveVersion(context.Background(), &pb.CredentialRetrieveVersionRequest{Name: "n"})
	if codeOf(err) != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", codeOf(err))
	}
}

func TestCredentialRetrieveVersion_Expired_Proto(t *testing.T) {
	svc := &fakeCredentialServicerProto{
		retrieveVersFn: func(_ context.Context, _ string, _ uint64) ([]byte, error) {
			return nil, credentials.ErrVersionExpired
		},
	}
	s := &qrdbServiceServer{credentialSvc: svc}

	_, err := s.CredentialRetrieveVersion(context.Background(), &pb.CredentialRetrieveVersionRequest{Name: "n", Version: 1})
	if codeOf(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", codeOf(err))
	}
}

// ---------------------------------------------------------------------------
// CredentialListVersions
// ---------------------------------------------------------------------------

func TestCredentialListVersions_Success_Proto(t *testing.T) {
	created := time.Unix(1_700_000_000, 0).UTC()
	grace := time.Unix(1_700_000_500, 0).UTC()
	svc := &fakeCredentialServicerProto{
		listVersionsFn: func(_ context.Context, _ string) ([]credentials.CredentialVersion, error) {
			return []credentials.CredentialVersion{
				{Name: "n", Version: 3, IsCurrent: true},
				{Name: "n", Version: 2, CreatedAt: created, GraceUntil: grace},
			}, nil
		},
	}
	s := &qrdbServiceServer{credentialSvc: svc}

	resp, err := s.CredentialListVersions(context.Background(), &pb.CredentialListVersionsRequest{Name: "n"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetName() != "n" {
		t.Errorf("unexpected name: %q", resp.GetName())
	}
	vs := resp.GetVersions()
	if len(vs) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(vs))
	}
	if vs[0].GetVersion() != 3 || !vs[0].GetIsCurrent() {
		t.Errorf("unexpected current version: %+v", vs[0])
	}
	if vs[0].GetCreatedAtUnix() != 0 || vs[0].GetGraceUntilUnix() != 0 {
		t.Errorf("expected zero timestamps on current version, got %+v", vs[0])
	}
	if vs[1].GetVersion() != 2 || vs[1].GetIsCurrent() {
		t.Errorf("unexpected historical version: %+v", vs[1])
	}
	if vs[1].GetCreatedAtUnix() != created.Unix() || vs[1].GetGraceUntilUnix() != grace.Unix() {
		t.Errorf("expected created=%d grace=%d, got %+v", created.Unix(), grace.Unix(), vs[1])
	}
}

func TestCredentialListVersions_NilService_Proto(t *testing.T) {
	s := &qrdbServiceServer{}
	_, err := s.CredentialListVersions(context.Background(), &pb.CredentialListVersionsRequest{Name: "n"})
	if codeOf(err) != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", codeOf(err))
	}
}

func TestCredentialListVersions_NotFound_Proto(t *testing.T) {
	svc := &fakeCredentialServicerProto{
		listVersionsFn: func(_ context.Context, _ string) ([]credentials.CredentialVersion, error) {
			return nil, credentials.ErrCredentialNotFound
		},
	}
	s := &qrdbServiceServer{credentialSvc: svc}

	_, err := s.CredentialListVersions(context.Background(), &pb.CredentialListVersionsRequest{Name: "missing"})
	if codeOf(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", codeOf(err))
	}
}

// ---------------------------------------------------------------------------
// CredentialGCExpired
// ---------------------------------------------------------------------------

func TestCredentialGCExpired_Success_Proto(t *testing.T) {
	svc := &fakeCredentialServicerProto{
		gcExpiredFn: func(_ context.Context, _ string) (int, error) {
			return 4, nil
		},
	}
	s := &qrdbServiceServer{credentialSvc: svc}

	resp, err := s.CredentialGCExpired(context.Background(), &pb.CredentialGCRequest{Name: "n"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetReaped() != 4 {
		t.Errorf("expected reaped=4, got %d", resp.GetReaped())
	}
}

func TestCredentialGCExpired_NilService_Proto(t *testing.T) {
	s := &qrdbServiceServer{}
	_, err := s.CredentialGCExpired(context.Background(), &pb.CredentialGCRequest{Name: "n"})
	if codeOf(err) != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", codeOf(err))
	}
}

func TestCredentialGCExpired_InvalidStrategy_Proto(t *testing.T) {
	svc := &fakeCredentialServicerProto{
		gcExpiredFn: func(_ context.Context, _ string) (int, error) {
			return 0, credentials.ErrInvalidStrategy
		},
	}
	s := &qrdbServiceServer{credentialSvc: svc}

	_, err := s.CredentialGCExpired(context.Background(), &pb.CredentialGCRequest{Name: "n"})
	if codeOf(err) != codes.Internal {
		t.Fatalf("expected Internal, got %s", codeOf(err))
	}
}

// ---------------------------------------------------------------------------
// mapServiceError fallback
// ---------------------------------------------------------------------------

func TestMapCredentialError_UnknownMapsToInternal_Proto(t *testing.T) {
	err := mapServiceError(errors.New("unexpected"))
	if codeOf(err) != codes.Internal {
		t.Fatalf("expected Internal, got %s", codeOf(err))
	}
}

func TestMapCredentialError_NilReturnsNil_Proto(t *testing.T) {
	if err := mapServiceError(nil); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

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
	"bytes"
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/keywrap"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// dekStores resolves one in-memory store per tenant.
type dekStores map[string]*kvstore.MemoryKVStore

func (d dekStores) Resolve(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	s, ok := d[tenantID]
	if !ok {
		s = kvstore.NewMemoryKVStore()
		d[tenantID] = s
	}
	return s, nil
}

// fakeDEKKeyWrapProvider is a same-shaped keywrap.Provider test double --
// it "wraps" by copying bytes verbatim, the identical shape
// pkg/keywrap's own fakeHardwareProvider test double uses.
type fakeDEKKeyWrapProvider struct{}

func (fakeDEKKeyWrapProvider) ProviderID() string { return keywrap.SoftwareProviderID }

func (fakeDEKKeyWrapProvider) Wrap(_ context.Context, _ string, dek []byte) ([]byte, error) {
	out := make([]byte, len(dek))
	copy(out, dek)
	return out, nil
}

func (fakeDEKKeyWrapProvider) Unwrap(_ context.Context, _ string, wrapped []byte) ([]byte, error) {
	out := make([]byte, len(wrapped))
	copy(out, wrapped)
	return out, nil
}

// testDEKKeyWrapRegistry returns a Registry whose "software" entry is
// fakeDEKKeyWrapProvider, so a test gets a fully working DEK keyring with
// no filesystem key directory.
func testDEKKeyWrapRegistry() keywrap.Registry {
	r := keywrap.NewRegistry()
	_ = r.Register(keywrap.SoftwareProviderID, func() (keywrap.Provider, error) {
		return fakeDEKKeyWrapProvider{}, nil
	})
	return r
}

func newDEKServiceServer(t *testing.T) *qrdbServiceServer {
	t.Helper()
	keyring, err := dek.NewKeyring(dekStores{}, dek.WithKeyWrapRegistry(testDEKKeyWrapRegistry()))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := service.NewDEKService(keyring)
	if err != nil {
		t.Fatal(err)
	}
	return &qrdbServiceServer{dekSvc: svc}
}

func TestTenantDEKRPCsRoundTrip(t *testing.T) {
	s := newDEKServiceServer(t)
	ctx := tenant.WithTenantID(context.Background(), "acme")
	aad := []byte("acme/secret/v1")

	enc, err := s.TenantDEKEncrypt(ctx, &pb.TenantDEKEncryptRequest{Plaintext: []byte("s3cret"), Aad: aad})
	if err != nil || enc.GetVersion() != 1 {
		t.Fatalf("encrypt = %v, %v", enc, err)
	}
	st, err := s.TenantDEKRotate(ctx, &pb.TenantDEKRequest{})
	if err != nil || st.GetCurrentVersion() != 2 || st.GetTenantId() != "acme" {
		t.Fatalf("rotate = %v, %v", st, err)
	}
	rw, err := s.TenantDEKRewrap(ctx, &pb.TenantDEKRewrapRequest{Ciphertext: enc.GetCiphertext(), Aad: aad})
	if err != nil || rw.GetFromVersion() != 1 || rw.GetToVersion() != 2 {
		t.Fatalf("rewrap = %v, %v", rw, err)
	}
	st, err = s.TenantDEKRetire(ctx, &pb.TenantDEKRetireRequest{Version: 1})
	if err != nil || st.GetVersions()[0].GetRetiredAtUnix() == 0 || st.GetVersions()[1].GetRetiredAtUnix() != 0 {
		t.Fatalf("retire = %v, %v", st, err)
	}
	dec, err := s.TenantDEKDecrypt(ctx, &pb.TenantDEKDecryptRequest{Ciphertext: rw.GetCiphertext(), Aad: aad})
	if err != nil || !bytes.Equal(dec.GetPlaintext(), []byte("s3cret")) || dec.GetVersion() != 2 {
		t.Fatalf("decrypt = %v, %v", dec, err)
	}
	if st, err = s.TenantDEKStatus(ctx, &pb.TenantDEKRequest{TenantId: "acme"}); err != nil || len(st.GetVersions()) != 2 {
		t.Fatalf("status = %v, %v", st, err)
	}
}

func TestTenantDEKRPCsMapRefusals(t *testing.T) {
	s := newDEKServiceServer(t)
	ctx := tenant.WithTenantID(context.Background(), "acme")
	enc, err := s.TenantDEKEncrypt(ctx, &pb.TenantDEKEncryptRequest{Plaintext: []byte("x"), Aad: []byte("a")})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		err  error
		want codes.Code
	}{
		"cross tenant":    {second(s.TenantDEKStatus(ctx, &pb.TenantDEKRequest{TenantId: "globex"})), codes.PermissionDenied},
		"aad mismatch":    {second(s.TenantDEKDecrypt(ctx, &pb.TenantDEKDecryptRequest{Ciphertext: enc.GetCiphertext(), Aad: []byte("b")})), codes.InvalidArgument},
		"retire current":  {second(s.TenantDEKRetire(ctx, &pb.TenantDEKRetireRequest{Version: 1})), codes.FailedPrecondition},
		"unknown version": {second(s.TenantDEKRetire(ctx, &pb.TenantDEKRetireRequest{Version: 7})), codes.NotFound},
		"malformed":       {second(s.TenantDEKRewrap(ctx, &pb.TenantDEKRewrapRequest{Ciphertext: []byte("junk")})), codes.InvalidArgument},
	}
	for name, c := range cases {
		if got := status.Code(c.err); got != c.want {
			t.Errorf("%s: code %v (%v), want %v", name, got, c.err, c.want)
		}
	}
}

func TestTenantDEKRPCsUnconfigured(t *testing.T) {
	s := &qrdbServiceServer{}
	ctx := context.Background()
	for name, err := range map[string]error{
		"status":  second(s.TenantDEKStatus(ctx, &pb.TenantDEKRequest{})),
		"rotate":  second(s.TenantDEKRotate(ctx, &pb.TenantDEKRequest{})),
		"retire":  second(s.TenantDEKRetire(ctx, &pb.TenantDEKRetireRequest{})),
		"encrypt": second(s.TenantDEKEncrypt(ctx, &pb.TenantDEKEncryptRequest{})),
		"decrypt": second(s.TenantDEKDecrypt(ctx, &pb.TenantDEKDecryptRequest{})),
		"rewrap":  second(s.TenantDEKRewrap(ctx, &pb.TenantDEKRewrapRequest{})),
	} {
		if status.Code(err) != codes.Unimplemented {
			t.Errorf("%s: %v, want Unimplemented", name, err)
		}
	}
}

// TestServiceErrorCodeClassifiesKeyringErrors: transports that render
// ServiceErrorCode (MCP) see the keyring's classification.
func TestServiceErrorCodeClassifiesKeyringErrors(t *testing.T) {
	if got := ServiceErrorCode(&dek.CrossTenantError{CallerTenant: "a", RequestedTenant: "b"}); got != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", got)
	}
	if got := status.Code(mapServiceError(errors.New("foreign"))); got != codes.Internal {
		t.Fatalf("foreign error code = %v, want Internal", got)
	}
}

func second[T any](_ T, err error) error { return err }

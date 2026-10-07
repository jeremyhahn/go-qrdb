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
	"net"
	"testing"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/keywrap"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

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

// connectDEKTransport serves the REAL gRPC handlers over the production
// keyring service on loopback and connects the SDK client to them.
func connectDEKTransport(t *testing.T) *Transport {
	t.Helper()
	keyring, err := dek.NewKeyring(dekStores{}, dek.WithKeyWrapRegistry(testDEKKeyWrapRegistry()))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := service.NewDEKService(keyring)
	if err != nil {
		t.Fatal(err)
	}
	srv := googlegrpc.NewServer()
	grpcpkg.RegisterService(srv, nil, nil, grpcpkg.WithRegisterDEKService(svc))
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	tr, cleanup := connectCredentialTransport(t, lis.Addr().String())
	t.Cleanup(cleanup)
	return tr
}

func TestTransportDEK_LifecycleOverTheWire(t *testing.T) {
	tr := connectDEKTransport(t)
	ctx := context.Background()
	aad := []byte("path=db/password|v=1")

	enc, err := tr.TenantDEKEncrypt(ctx, &transport.TenantDEKEncryptRequest{Plaintext: []byte("s3cret"), AAD: aad})
	if err != nil || enc.Version != 1 {
		t.Fatalf("encrypt = %+v, %v", enc, err)
	}
	st, err := tr.TenantDEKRotate(ctx, &transport.TenantDEKRequest{})
	if err != nil || st.CurrentVersion != 2 || len(st.Versions) != 2 {
		t.Fatalf("rotate = %+v, %v", st, err)
	}
	dec, err := tr.TenantDEKDecrypt(ctx, &transport.TenantDEKDecryptRequest{Ciphertext: enc.Ciphertext, AAD: aad})
	if err != nil || !bytes.Equal(dec.Plaintext, []byte("s3cret")) || dec.Version != 1 {
		t.Fatalf("decrypt old version = %+v, %v", dec, err)
	}
	rw, err := tr.TenantDEKRewrap(ctx, &transport.TenantDEKRewrapRequest{Ciphertext: enc.Ciphertext, AAD: aad})
	if err != nil || rw.FromVersion != 1 || rw.ToVersion != 2 {
		t.Fatalf("rewrap = %+v, %v", rw, err)
	}
	st, err = tr.TenantDEKRetire(ctx, &transport.TenantDEKRetireRequest{Version: 1})
	if err != nil || st.Versions[0].RetiredAt == nil {
		t.Fatalf("retire = %+v, %v", st, err)
	}
	if st, err = tr.TenantDEKStatus(ctx, &transport.TenantDEKRequest{}); err != nil || st.CurrentVersion != 2 {
		t.Fatalf("status = %+v, %v", st, err)
	}
}

// TestTransportDEK_RefusalsCarryTheirCodes pins the shared classification as
// it arrives at a gRPC client.
func TestTransportDEK_RefusalsCarryTheirCodes(t *testing.T) {
	tr := connectDEKTransport(t)
	ctx := context.Background()
	enc, err := tr.TenantDEKEncrypt(ctx, &transport.TenantDEKEncryptRequest{Plaintext: []byte("x"), AAD: []byte("a")})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		err  error
		want codes.Code
	}{
		"cross tenant":   {second(tr.TenantDEKStatus(ctx, &transport.TenantDEKRequest{TenantID: "globex"})), codes.PermissionDenied},
		"aad mismatch":   {second(tr.TenantDEKDecrypt(ctx, &transport.TenantDEKDecryptRequest{Ciphertext: enc.Ciphertext, AAD: []byte("b")})), codes.InvalidArgument},
		"retire current": {second(tr.TenantDEKRetire(ctx, &transport.TenantDEKRetireRequest{Version: 1})), codes.FailedPrecondition},
		"malformed":      {second(tr.TenantDEKRewrap(ctx, &transport.TenantDEKRewrapRequest{Ciphertext: []byte("junk")})), codes.InvalidArgument},
	}
	for name, c := range cases {
		var re *transport.RequestError
		if !errors.As(c.err, &re) || re.StatusCode != int(c.want) {
			t.Errorf("%s: err = %v, want a RequestError with code %v", name, c.err, c.want)
		}
	}
}

func TestTransportDEK_NilRequests(t *testing.T) {
	tr := connectDEKTransport(t)
	ctx := context.Background()
	for op, err := range map[string]error{
		"status":  second(tr.TenantDEKStatus(ctx, nil)),
		"rotate":  second(tr.TenantDEKRotate(ctx, nil)),
		"retire":  second(tr.TenantDEKRetire(ctx, nil)),
		"encrypt": second(tr.TenantDEKEncrypt(ctx, nil)),
		"decrypt": second(tr.TenantDEKDecrypt(ctx, nil)),
		"rewrap":  second(tr.TenantDEKRewrap(ctx, nil)),
	} {
		var nre *transport.NilRequestError
		if !errors.As(err, &nre) {
			t.Errorf("%s(nil): %v", op, err)
		}
	}
}

func second[T any](_ T, err error) error { return err }

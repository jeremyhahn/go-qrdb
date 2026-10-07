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
	"bytes"
	"context"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/keywrap"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
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

func newDEKServer(t *testing.T) *Server {
	t.Helper()
	keyring, err := dek.NewKeyring(dekStores{}, dek.WithKeyWrapRegistry(testDEKKeyWrapRegistry()))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := service.NewDEKService(keyring)
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0", WithDEKService(svc))
}

// dekFrame serves one dek.* frame through the registered handler.
func dekFrame(t *testing.T, s *Server, method string, in, out proto.Message) error {
	t.Helper()
	h, ok := s.handlers[method]
	if !ok {
		t.Fatalf("%s is not registered", method)
	}
	payload, err := proto.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := h(tenant.WithTenantID(context.Background(), "acme"), payload)
	if err != nil {
		return err
	}
	if err := proto.Unmarshal(reply, out); err != nil {
		t.Fatalf("unmarshal %s reply: %v", method, err)
	}
	return nil
}

func TestHandleDEK_FramesRoundTrip(t *testing.T) {
	s := newDEKServer(t)
	aad := []byte("a")
	var enc pb.TenantDEKEncryptResponse
	if err := dekFrame(t, s, "dek.Encrypt", &pb.TenantDEKEncryptRequest{Plaintext: []byte("s3cret"), Aad: aad}, &enc); err != nil {
		t.Fatal(err)
	}
	var st pb.TenantDEKStatusResponse
	if err := dekFrame(t, s, "dek.Rotate", &pb.TenantDEKRequest{}, &st); err != nil || st.GetCurrentVersion() != 2 {
		t.Fatalf("rotate = %v, %v", &st, err)
	}
	var rw pb.TenantDEKRewrapResponse
	if err := dekFrame(t, s, "dek.Rewrap", &pb.TenantDEKRewrapRequest{Ciphertext: enc.GetCiphertext(), Aad: aad}, &rw); err != nil || rw.GetToVersion() != 2 {
		t.Fatalf("rewrap = %v, %v", &rw, err)
	}
	if err := dekFrame(t, s, "dek.Retire", &pb.TenantDEKRetireRequest{Version: 1}, &st); err != nil || st.GetVersions()[0].GetRetiredAtUnix() == 0 {
		t.Fatalf("retire = %v, %v", &st, err)
	}
	var dec pb.TenantDEKDecryptResponse
	if err := dekFrame(t, s, "dek.Decrypt", &pb.TenantDEKDecryptRequest{Ciphertext: rw.GetCiphertext(), Aad: aad}, &dec); err != nil || !bytes.Equal(dec.GetPlaintext(), []byte("s3cret")) {
		t.Fatalf("decrypt = %v, %v", &dec, err)
	}
	if err := dekFrame(t, s, "dek.Status", &pb.TenantDEKRequest{}, &st); err != nil || st.GetTenantId() != "acme" {
		t.Fatalf("status = %v, %v", &st, err)
	}
}

// TestHandleDEK_ErrorsMapToTheSharedStatuses checks the wire statuses QUIC
// answers for keyring refusals.
func TestHandleDEK_ErrorsMapToTheSharedStatuses(t *testing.T) {
	s := newDEKServer(t)
	var st pb.TenantDEKStatusResponse
	err := dekFrame(t, s, "dek.Status", &pb.TenantDEKRequest{TenantId: "globex"}, &st)
	if code, _ := mapServiceError(err); code != wire.StatusPermissionDenied {
		t.Errorf("cross tenant: status %d, want PermissionDenied", code)
	}
	if err := dekFrame(t, s, "dek.Rotate", &pb.TenantDEKRequest{}, &st); err != nil {
		t.Fatal(err)
	}
	err = dekFrame(t, s, "dek.Retire", &pb.TenantDEKRetireRequest{Version: 1}, &st)
	if code, _ := mapServiceError(err); code != wire.StatusFailedPrecondition {
		t.Errorf("retire current: status %d, want FailedPrecondition", code)
	}
	var dec pb.TenantDEKDecryptResponse
	err = dekFrame(t, s, "dek.Decrypt", &pb.TenantDEKDecryptRequest{Ciphertext: []byte("junk")}, &dec)
	if code, _ := mapServiceError(err); code != wire.StatusInvalidArgument {
		t.Errorf("malformed: status %d, want InvalidArgument", code)
	}
}

func TestHandleDEK_UndecodableFramesAreInvalidArgument(t *testing.T) {
	s := newDEKServer(t)
	for _, method := range []string{"dek.Status", "dek.Rotate", "dek.Retire", "dek.Encrypt", "dek.Decrypt", "dek.Rewrap"} {
		_, err := s.handlers[method](context.Background(), []byte{0xff, 0xff, 0xff})
		if code, _ := mapServiceError(err); code != wire.StatusInvalidArgument {
			t.Errorf("%s: status %d, want InvalidArgument", method, code)
		}
	}
}

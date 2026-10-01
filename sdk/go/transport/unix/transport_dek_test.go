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

package unix

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
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

// connectDEKTransport starts the REAL Unix server over the production
// keyring service and connects the SDK client to it.
func connectDEKTransport(t *testing.T) *Transport {
	t.Helper()
	keyring, err := dek.NewKeyring(dekStores{})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := service.NewDEKService(keyring)
	if err != nil {
		t.Fatal(err)
	}
	socketPath := tempSocketPath(t)
	srv := unixserver.NewServer(newMockKVService(), newMockAdminService(), socketPath,
		unixserver.WithDEKService(svc),
		unixserver.WithTLS(testUnixServerTLS),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("start Unix server: %v", err)
	}
	t.Cleanup(func() { srv.Stop() })
	tr, err := New(transport.WithAddress(socketPath), testClientTLSOption())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

func TestTransportDEK_LifecycleOverTheSocket(t *testing.T) {
	tr := connectDEKTransport(t)
	ctx := context.Background()
	aad := []byte("path=db/password|v=1")

	enc, err := tr.TenantDEKEncrypt(ctx, &transport.TenantDEKEncryptRequest{Plaintext: []byte("s3cret"), AAD: aad})
	if err != nil || enc.Version != 1 {
		t.Fatalf("encrypt = %+v, %v", enc, err)
	}
	if _, err := tr.TenantDEKRotate(ctx, &transport.TenantDEKRequest{}); err != nil {
		t.Fatal(err)
	}
	rw, err := tr.TenantDEKRewrap(ctx, &transport.TenantDEKRewrapRequest{Ciphertext: enc.Ciphertext, AAD: aad})
	if err != nil || rw.ToVersion != 2 {
		t.Fatalf("rewrap = %+v, %v", rw, err)
	}
	if _, err := tr.TenantDEKRetire(ctx, &transport.TenantDEKRetireRequest{Version: 1}); err != nil {
		t.Fatal(err)
	}
	dec, err := tr.TenantDEKDecrypt(ctx, &transport.TenantDEKDecryptRequest{Ciphertext: rw.Ciphertext, AAD: aad})
	if err != nil || !bytes.Equal(dec.Plaintext, []byte("s3cret")) {
		t.Fatalf("decrypt = %+v, %v", dec, err)
	}
	st, err := tr.TenantDEKStatus(ctx, &transport.TenantDEKRequest{})
	if err != nil || st.CurrentVersion != 2 || st.Versions[0].RetiredAt == nil {
		t.Fatalf("status = %+v, %v", st, err)
	}
}

func TestTransportDEK_CrossTenantIsPermissionDenied(t *testing.T) {
	tr := connectDEKTransport(t)
	_, err := tr.TenantDEKEncrypt(context.Background(), &transport.TenantDEKEncryptRequest{TenantID: "globex", Plaintext: []byte("x")})
	var re *transport.RequestError
	if !errors.As(err, &re) || re.StatusCode != int(codes.PermissionDenied) {
		t.Fatalf("err = %v, want PermissionDenied", err)
	}
	if _, err := tr.TenantDEKStatus(context.Background(), nil); err == nil {
		t.Fatal("a nil request must fail")
	}
}

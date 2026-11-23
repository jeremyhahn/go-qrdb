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
	"crypto/tls"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// stubSealStore is an in-memory service.SealStoreServicer that counts calls.
type stubSealStore struct {
	mu       sync.Mutex
	secrets  map[string][]byte
	resealed []string
	calls    int
}

var errStubSealStoreMissing = errors.New("stub seal store: no such secret")

func newStubSealStore() *stubSealStore {
	return &stubSealStore{secrets: map[string][]byte{}}
}

func (s *stubSealStore) Put(_ context.Context, name string, secret []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return &seal.SealStoreError{Op: "put", Key: name, Err: seal.ErrInvalidSecretName}
	}
	s.secrets[name] = slices.Clone(secret)
	return nil
}

func (s *stubSealStore) Get(_ context.Context, name string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	v, ok := s.secrets[name]
	if !ok {
		return nil, errStubSealStoreMissing
	}
	return slices.Clone(v), nil
}

func (s *stubSealStore) Delete(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	delete(s.secrets, name)
	return nil
}

// names lists the stored secret names, sorted. The caller must hold s.mu.
func (s *stubSealStore) names() []string {
	names := make([]string, 0, len(s.secrets))
	for n := range s.secrets {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// secret returns a copy of the named secret, read under s.mu.
func (s *stubSealStore) secret(name string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.secrets[name]
	return slices.Clone(v), ok
}

// resealNames returns a copy of the resealed names, read under s.mu.
func (s *stubSealStore) resealNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.resealed)
}

// snapshot returns the call count and the number of stored secrets, read under s.mu.
func (s *stubSealStore) snapshot() (calls, stored int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, len(s.secrets)
}

func (s *stubSealStore) List(_ context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.names(), nil
}

func (s *stubSealStore) Reseal(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.resealed = append(s.resealed, name)
	return nil
}

func (s *stubSealStore) Status(_ context.Context) (*seal.SealStoreStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	names := s.names()
	return &seal.SealStoreStatus{Available: true, Strategy: seal.StrategyID("stub"), SecretCount: len(names), SecretNames: names}, nil
}

// sealStoreServer starts a QUIC server serving stub as the seal store to the
// fixed caller id and returns its address and the client TLS config.
func sealStoreServer(t *testing.T, id *auth.Identity, stub *stubSealStore) (string, *tls.Config) {
	t.Helper()
	caller := authtest.NewFixedAuthenticator(id)
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme")
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles), authmw.WithAuthenticator(caller))
	serverCfg, clientCfg := generateCredentialE2ETLS(t)
	serverCfg.ClientAuth = tls.RequireAnyClientCert
	clientCfg.Certificates = serverCfg.Certificates
	opts := []ServerOption{WithTLS(serverCfg), WithAuthMiddleware(mw)}
	if stub != nil {
		opts = append(opts, WithSealStoreService(stub))
	}
	srv := NewServer(nil, nil, "127.0.0.1:0", opts...)
	if err := srv.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(srv.Stop)
	return srv.Addr(), clientCfg
}

func sealStoreCall(t *testing.T, addr string, cfg *tls.Config, method string, req any) (uint16, []byte) {
	t.Helper()
	var payload []byte
	if req != nil {
		var err error
		if payload, err = json.Marshal(req); err != nil {
			t.Fatalf("marshal: %v", err)
		}
	}
	return credentialE2ERoundTrip(t, addr, cfg, method, payload)
}

func wantSealStoreOK(t *testing.T, method string, status uint16, body []byte) {
	t.Helper()
	if status != wire.StatusOK {
		t.Fatalf("%s status = %s (%s), want OK", method, wire.StatusName(status), body)
	}
}

func TestSealStoreOverQUIC(t *testing.T) {
	stub := newStubSealStore()
	addr, cfg := sealStoreServer(t, authtest.SOIdentity(tenantid.System, "so"), stub)
	secret := []byte{0x00, 0x01, 0xfe, 0xff}

	status, body := sealStoreCall(t, addr, cfg, "sealstore.Put", map[string]any{"name": "jwt", "secret": secret})
	wantSealStoreOK(t, "Put", status, body)
	if got, _ := stub.secret("jwt"); !slices.Equal(got, secret) {
		t.Fatalf("stub secret = %x, want %x", got, secret)
	}
	sealStoreCall(t, addr, cfg, "sealstore.Put", map[string]any{"name": "tls", "secret": []byte("k")})

	status, body = sealStoreCall(t, addr, cfg, "sealstore.Get", map[string]any{"name": "jwt"})
	wantSealStoreOK(t, "Get", status, body)
	var got transport.SealStoreGetResponse
	if err := json.Unmarshal(body, &got); err != nil || got.Name != "jwt" || !slices.Equal(got.Secret, secret) {
		t.Fatalf("Get = %+v, %v", got, err)
	}

	status, body = sealStoreCall(t, addr, cfg, "sealstore.List", nil)
	wantSealStoreOK(t, "List", status, body)
	var list transport.SealStoreListResponse
	if err := json.Unmarshal(body, &list); err != nil || !slices.Equal(list.Names, []string{"jwt", "tls"}) {
		t.Fatalf("List = %+v, %v", list, err)
	}

	status, body = sealStoreCall(t, addr, cfg, "sealstore.Reseal", map[string]any{"name": "tls"})
	wantSealStoreOK(t, "Reseal", status, body)
	if resealed := stub.resealNames(); !slices.Equal(resealed, []string{"tls"}) {
		t.Fatalf("resealed = %v, want [tls]", resealed)
	}

	status, body = sealStoreCall(t, addr, cfg, "sealstore.Status", nil)
	wantSealStoreOK(t, "Status", status, body)
	var st transport.SealStoreStatusResponse
	if err := json.Unmarshal(body, &st); err != nil || !st.Available || st.SealerID != "stub" ||
		st.SecretCount != 2 || !slices.Equal(st.SecretNames, []string{"jwt", "tls"}) {
		t.Fatalf("Status = %+v, %v", st, err)
	}

	status, body = sealStoreCall(t, addr, cfg, "sealstore.Delete", map[string]any{"name": "jwt"})
	wantSealStoreOK(t, "Delete", status, body)
	if _, ok := stub.secret("jwt"); ok {
		t.Fatal("jwt still stored after Delete")
	}
}

func TestSealStoreOverQUIC_TenantAdminDenied(t *testing.T) {
	stub := newStubSealStore()
	addr, cfg := sealStoreServer(t, authtest.AdminIdentity("acme", "acme-admin"), stub)
	status, _ := sealStoreCall(t, addr, cfg, "sealstore.Put", map[string]any{"name": "jwt", "secret": []byte("x")})
	if status != wire.StatusPermissionDenied {
		t.Fatalf("status = %s, want PERMISSION_DENIED", wire.StatusName(status))
	}
	calls, stored := stub.snapshot()
	if calls != 0 || stored != 0 {
		t.Fatalf("a denied call reached the seal store: calls=%d", calls)
	}
}

func TestSealStoreOverQUIC_Errors(t *testing.T) {
	so := authtest.SOIdentity(tenantid.System, "so")
	t.Run("not configured", func(t *testing.T) {
		addr, cfg := sealStoreServer(t, so, nil)
		if status, _ := sealStoreCall(t, addr, cfg, "sealstore.List", nil); status == wire.StatusOK {
			t.Fatal("List without a seal store answered OK")
		}
	})
	t.Run("bad JSON and service errors", func(t *testing.T) {
		stub := newStubSealStore()
		addr, cfg := sealStoreServer(t, so, stub)
		if status, _ := credentialE2ERoundTrip(t, addr, cfg, "sealstore.Put", []byte("not json")); status == wire.StatusOK {
			t.Fatal("Put with bad JSON answered OK")
		}
		if status, _ := sealStoreCall(t, addr, cfg, "sealstore.Get", map[string]any{"name": "missing"}); status == wire.StatusOK {
			t.Fatal("Get of a missing secret answered OK")
		}
	})
	t.Run("invalid name", func(t *testing.T) {
		addr, cfg := sealStoreServer(t, so, newStubSealStore())
		status, _ := sealStoreCall(t, addr, cfg, "sealstore.Put", map[string]any{"name": `a\b`, "secret": []byte("x")})
		if status != wire.StatusInvalidArgument {
			t.Fatalf("Put(a\\b): status %v, want StatusInvalidArgument", status)
		}
	})
}

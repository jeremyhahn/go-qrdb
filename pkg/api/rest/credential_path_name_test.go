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

package rest

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	resttransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/rest"
)

// namedCredentialService is a name-keyed credential store: every call
// answers for exactly the name the handler passes, so a name that the path
// mangles finds nothing and fails the test.
type namedCredentialService struct {
	*coverageCredentialService
	mu       sync.Mutex
	values   map[string][]byte
	policies map[string]credentials.RotationPolicy
	resolved map[string]credentials.MigrationResolution
}

func newNamedCredentialService() *namedCredentialService {
	return &namedCredentialService{
		coverageCredentialService: newCoverageCredentialService(),
		values:                    map[string][]byte{},
		policies:                  map[string]credentials.RotationPolicy{},
		resolved:                  map[string]credentials.MigrationResolution{},
	}
}

func (m *namedCredentialService) CredentialStore(_ context.Context, name string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[name] = append([]byte(nil), value...)
	return nil
}

func (m *namedCredentialService) CredentialRetrieveBuffer(_ context.Context, name string) (*secure.Buffer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.values[name]
	if !ok {
		return nil, credentials.ErrCredentialNotFound
	}
	return secure.NewBufferFromBytes(append([]byte(nil), v...))
}

func (m *namedCredentialService) CredentialSetRotationPolicy(_ context.Context, name string, policy credentials.RotationPolicy) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.values[name]; !ok {
		return credentials.ErrCredentialNotFound
	}
	m.policies[name] = policy
	return nil
}

func (m *namedCredentialService) CredentialRotationPolicy(_ context.Context, name string) (*service.CredentialRotationPolicyStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.policies[name]
	if !ok {
		return nil, credentials.ErrCredentialNotFound
	}
	return &service.CredentialRotationPolicyStatus{Policy: p}, nil
}

func (m *namedCredentialService) CredentialResolveMigrationConflict(_ context.Context, name string, resolution credentials.MigrationResolution) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.values[name]; !ok {
		return credentials.ErrMigrationConflictNotFound
	}
	m.resolved[name] = resolution
	return nil
}

// TestCredentialNameWithSlashOrPercent_ReachesEveryRoute drives the SDK REST
// transport against the server's full handler chain (canonical-path guard,
// router, credential handlers): a name holding "/" or "%" stays one path
// segment and reaches the service unchanged.
func TestCredentialNameWithSlashOrPercent_ReachesEveryRoute(t *testing.T) {
	svc := newNamedCredentialService()
	srv := NewServer(&mockKVService{}, newDefaultAdminSvc(), "127.0.0.1:0", WithCredentialService(svc))
	hs := httptest.NewServer(srv.server.Handler)
	defer hs.Close()

	tr, err := resttransport.New(transport.WithAddress(hs.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := tr.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = tr.Close() }()

	for _, name := range []string{"db/main", "a%b", "a%2Fb", "x/y%z/"} {
		t.Run(name, func(t *testing.T) {
			want := []byte("value-of-" + name)
			if err := tr.CredentialStore(ctx, name, want); err != nil {
				t.Fatalf("CredentialStore: %v", err)
			}
			buf, err := tr.CredentialRetrieveBuffer(ctx, name)
			if err != nil {
				t.Fatalf("CredentialRetrieveBuffer: %v", err)
			}
			equal, err := buf.EqualBytes(want)
			buf.Destroy()
			if err != nil || !equal {
				t.Fatalf("retrieved value differs from %q (err %v)", want, err)
			}

			if err := tr.CredentialSetRotationPolicy(ctx, &transport.CredentialSetRotationPolicyRequest{
				Name: name, Enabled: true, Interval: "1h", RetainVersions: 3,
			}); err != nil {
				t.Fatalf("CredentialSetRotationPolicy: %v", err)
			}
			pol, err := tr.CredentialRotationPolicy(ctx, name)
			if err != nil {
				t.Fatalf("CredentialRotationPolicy: %v", err)
			}
			if !pol.Enabled || pol.RetainVersions != 3 {
				t.Fatalf("policy = %+v, want enabled with 3 versions", pol)
			}
			svc.mu.Lock()
			stored := svc.policies[name]
			svc.mu.Unlock()
			if stored.Interval != time.Hour {
				t.Fatalf("stored interval = %v, want 1h", stored.Interval)
			}

			if err := tr.CredentialResolveMigrationConflict(ctx, &transport.CredentialResolveMigrationConflictRequest{
				Name: name, Resolution: "discard",
			}); err != nil {
				t.Fatalf("CredentialResolveMigrationConflict: %v", err)
			}
			svc.mu.Lock()
			_, ok := svc.resolved[name]
			svc.mu.Unlock()
			if !ok {
				t.Fatalf("migration conflict for %q was not resolved under that name", name)
			}
		})
	}
}

// TestCredentialNameSegment_BadEscapeIs400 sends a segment whose inner escape
// is malformed: the handler refuses it instead of guessing a name.
func TestCredentialNameSegment_BadEscapeIs400(t *testing.T) {
	srv := NewServer(&mockKVService{}, newDefaultAdminSvc(), "127.0.0.1:0", WithCredentialService(newNamedCredentialService()))
	rec := serveRaw(srv, "GET", "/v1/credentials/a%25zz/value")
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

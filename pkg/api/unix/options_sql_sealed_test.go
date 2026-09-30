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
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// stubSQLService is a minimal service.SQLServicer used to verify the
// WithSQLService option and its Start-time registration.
type stubSQLService struct{}

func (stubSQLService) Execute(_ context.Context, _, _ string) (*service.SQLQueryResult, error) {
	return &service.SQLQueryResult{}, nil
}

var _ service.SQLServicer = stubSQLService{}

// fakeSealedProbe is a deterministic sealedWindowProbe test double.
type fakeSealedProbe struct{ sealed, enrollment bool }

func (f *fakeSealedProbe) EnrollmentOpen() bool { return f.enrollment }

func (f *fakeSealedProbe) IsSealed() bool { return f.sealed }

func TestWithSQLService_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-sql.sock",
		WithSQLService(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.sqlSvc != nil {
		t.Fatal("expected nil sqlSvc")
	}
}

func TestWithSQLService_NonNil(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-sql-nn.sock",
		WithSQLService(stubSQLService{}),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.sqlSvc == nil {
		t.Fatal("expected non-nil sqlSvc after WithSQLService")
	}
}

// TestStart_WithSQLService_Registered exercises the non-nil sqlSvc branch in
// Start so that SQLQuery is reachable over the Unix socket the same way it
// is over REST and TCP gRPC.
func TestStart_WithSQLService_Registered(t *testing.T) {
	socketPath := tempSocketPath(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath,
		WithSQLService(stubSQLService{}),
		WithTLS(selfSignedTLSConfig(t)))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	if srv.Addr() == "" {
		t.Fatal("expected non-empty Addr after Start")
	}
}

func TestWithSealedWindowProbe_Applied(t *testing.T) {
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-sealed.sock",
		WithSealedWindowProbe(nil),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.sealedWindowProbe != nil {
		t.Fatal("expected nil sealedWindowProbe")
	}
}

func TestWithSealedWindowProbe_NonNil(t *testing.T) {
	probe := &fakeSealedProbe{sealed: true}
	srv := NewServer(newMockKVService(), &mockAdminService{}, "/tmp/test-sealed-nn.sock",
		WithSealedWindowProbe(probe),
		WithTLS(selfSignedTLSConfig(t)))
	if srv.sealedWindowProbe == nil {
		t.Fatal("expected non-nil sealedWindowProbe after WithSealedWindowProbe")
	}
}

// TestStart_WithSealedWindowProbe_Registered exercises the non-nil
// sealedWindowProbe branch in Start so the interceptors are installed
// without crashing the server.
func TestStart_WithSealedWindowProbe_Registered(t *testing.T) {
	socketPath := tempSocketPath(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath,
		WithSealedWindowProbe(&fakeSealedProbe{sealed: false}),
		WithTLS(selfSignedTLSConfig(t)))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	if srv.Addr() == "" {
		t.Fatal("expected non-empty Addr after Start")
	}
}

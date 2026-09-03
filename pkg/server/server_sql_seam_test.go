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

package server

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// newSQLServiceFn seam (qrdb_sql build tag)
// ---------------------------------------------------------------------------

// saveNewSQLServiceFn snapshots the qrdb_sql seam and returns a restore
// function, mirroring saveSQLFns for the wire-protocol seams.
func saveNewSQLServiceFn() func() {
	orig := newSQLServiceFn
	return func() { newSQLServiceFn = orig }
}

// stubSQLService satisfies service.SQLServicer for seam tests.
type stubSQLService struct{}

func (s *stubSQLService) Execute(_ context.Context, _, _ string) (*service.SQLQueryResult, error) {
	return &service.SQLQueryResult{}, nil
}

var _ service.SQLServicer = (*stubSQLService)(nil)

func TestNewSQLServiceFn_CalledPerTransport(t *testing.T) {
	tests := []struct {
		name      string
		rest      bool
		grpc      bool
		wantCalls int64
	}{
		{name: "rest only", rest: true, grpc: false, wantCalls: 1},
		{name: "grpc only", rest: false, grpc: true, wantCalls: 1},
		{name: "rest and grpc", rest: true, grpc: true, wantCalls: 2},
		{name: "no transports", rest: false, grpc: false, wantCalls: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			restore := saveNewSQLServiceFn()
			defer restore()

			var calls atomic.Int64
			newSQLServiceFn = func(_ *Server) service.SQLServicer {
				calls.Add(1)
				return &stubSQLService{}
			}

			cfg := testConfig(t, tc.rest, tc.grpc)
			srv := New(cfg, newMockKVService(), &mockAdminService{},
				WithCeremonyCA(testServerCA),
				WithDatabaseService(&stubDatabaseService{}))

			if err := srv.Start(); err != nil {
				t.Fatalf("Start failed: %v", err)
			}
			defer srv.Stop()

			if got := calls.Load(); got != tc.wantCalls {
				t.Fatalf("expected newSQLServiceFn to be called %d time(s), got %d", tc.wantCalls, got)
			}
		})
	}
}

func TestNewSQLServiceFn_NilFn(t *testing.T) {
	restore := saveNewSQLServiceFn()
	defer restore()

	newSQLServiceFn = nil

	cfg := testConfig(t, true, true)
	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithCeremonyCA(testServerCA),
		WithDatabaseService(&stubDatabaseService{}))

	// Should not panic and should succeed: the SQL service is simply
	// absent and each transport surfaces its nil-service error.
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()
}

func TestNewSQLServiceFn_NotCalledWithoutDatabaseService(t *testing.T) {
	restore := saveNewSQLServiceFn()
	defer restore()

	var calls atomic.Int64
	newSQLServiceFn = func(_ *Server) service.SQLServicer {
		calls.Add(1)
		return &stubSQLService{}
	}

	cfg := testConfig(t, true, true)
	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithCeremonyCA(testServerCA))

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	if got := calls.Load(); got != 0 {
		t.Fatalf("expected newSQLServiceFn not to be called without a database service, got %d call(s)", got)
	}
}

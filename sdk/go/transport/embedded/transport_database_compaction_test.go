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

package embedded

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The embedded transport sets a policy for the context's tenant, and confines
// it -- the system tenant an unbound context resolves to included -- to its
// own databases.
func TestEmbeddedDatabase_CompactionPolicy_RealService(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := database.NewManager(logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	if _, err := mgr.Create(database.DatabaseConfig{Name: "acme-db", TenantID: "acme", Engine: database.EngineKV, Backend: database.BackendMemory}); err != nil {
		t.Fatal(err)
	}
	tr := newExtTransport(t, WithDatabaseService(servicetest.NewDatabaseService(t, mgr)))
	policy := &transport.DatabaseCompactionConfig{Mode: "revision", RetainRevisions: 2}

	var nf *database.NotFoundError
	if _, err := tr.UpdateDatabaseCompaction(context.Background(), transport.UpdateDatabaseCompactionRequest{
		Name: "acme-db", Compaction: policy,
	}); !errors.As(err, &nf) {
		t.Fatalf("an unbound (system) context on acme's database = %v, want NotFoundError", err)
	}

	info, err := tr.UpdateDatabaseCompaction(tenant.WithTenantID(context.Background(), "acme"), transport.UpdateDatabaseCompactionRequest{
		Name: "acme-db", Compaction: policy,
	})
	if err != nil {
		t.Fatalf("acme on its own database: %v", err)
	}
	if info.Compaction == nil || info.Compaction.Mode != "revision" || info.Compaction.RetainRevisions != 2 {
		t.Fatalf("Compaction = %+v", info.Compaction)
	}

	var invalid *compaction.InvalidPolicyError
	if _, err := tr.UpdateDatabaseCompaction(tenant.WithTenantID(context.Background(), "acme"), transport.UpdateDatabaseCompactionRequest{
		Name: "acme-db", Compaction: &transport.DatabaseCompactionConfig{Mode: "weekly"},
	}); !errors.As(err, &invalid) {
		t.Fatalf("weekly = %v, want *InvalidCompactionPolicyError", err)
	}
}

func TestEmbeddedDatabase_UpdateCompaction_NilService(t *testing.T) {
	tr := newExtTransport(t)
	var nilSvc *NilServiceError
	if _, err := tr.UpdateDatabaseCompaction(context.Background(), transport.UpdateDatabaseCompactionRequest{Name: "db"}); !errors.As(err, &nilSvc) {
		t.Fatalf("nil service = %v, want *NilServiceError", err)
	}
}

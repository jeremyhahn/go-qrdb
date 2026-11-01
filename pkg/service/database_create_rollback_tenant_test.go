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

package service

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal/clustersealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
)

// clearFailsAfterSealer clears the first ok seal records and fails every
// later Clear with err.
type clearFailsAfterSealer struct {
	*clustersealtest.Sealer
	ok    int32
	calls *atomic.Int32
	err   error
}

func (s clearFailsAfterSealer) Clear(ctx context.Context, b clusterseal.Barrier, p clusterseal.Principal) (clusterseal.Record, error) {
	if s.calls.Add(1) > s.ok {
		return clusterseal.Record{}, s.err
	}
	return s.Sealer.Clear(ctx, b, p)
}

// A create that fails clearing its predecessor's seal record is rolled back
// by its own tenant and name: another tenant's open database of the same
// name does not make the rollback ambiguous, and is left alone.
func TestCreateForTenant_PredecessorSealFails_RollsBackTheTenantsOwnDatabase(t *testing.T) {
	mgr := databasetest.NewManager(slog.New(slog.DiscardHandler), t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	clearErr := errors.New("seal record clear refused")
	svc := NewDatabaseService(mgr, WithDatabaseClusterSealer(clearFailsAfterSealer{
		Sealer: clustersealtest.New(true), ok: 1, calls: new(atomic.Int32), err: clearErr,
	}))

	if _, err := svc.CreateForTenant(tenantCtx("acme"), testDBConfig("orders")); err != nil {
		t.Fatalf("CreateForTenant(acme) = %v", err)
	}
	if _, err := svc.CreateForTenant(tenantCtx("beta"), testDBConfig("orders")); !errors.Is(err, clearErr) {
		t.Fatalf("CreateForTenant(beta) = %v, want the seal clear failure", err)
	}
	if _, ok := mgr.Local("orders", "beta"); ok {
		t.Fatal("beta's failed create is still open, want it rolled back")
	}
	if _, ok := mgr.Local("orders", "acme"); !ok {
		t.Fatal("acme's database was dropped by beta's rollback")
	}
	if _, recorded, err := mgr.RecordedIncarnation(context.Background(), "beta", "orders"); err != nil || recorded {
		t.Fatal("beta's failed create is still recorded, want it rolled back")
	}
}

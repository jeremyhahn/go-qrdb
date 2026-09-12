//go:build qrdb_ts

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
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// assertTimeSeriesLeg is the time-series leg of the full-vertical tests in this
// package: TestRaftBackendFactory_DataDirUnderTenantRoot, TestRaftE2E_FullVertical
// and TestTenantHost_FullEngineIsolationAndEncryption, each of which proves its
// subject reaches ALL the engines rather than KV alone.
//
// It has a !qrdb_ts twin in timeseries_leg_stub_test.go, mirroring the
// pkg/timeseries service.go/stub.go pair it exercises, and the pair is why this
// leg lives in its own file at all. Written inline, it made those three tests
// FAIL under a plain `go test ./...`: the engine is compiled out without the tag
// and refuses at RUNTIME with timeseries.ErrNotCompiled, so the failure looked
// like a regression the reader had just introduced and cost three separate
// people a detour to disprove. `make test` sets the tag and never saw it.
//
// A runtime refusal is the wrong instrument for a compile-time fact, and the
// repository forbids the skip guard that would otherwise paper over it, so the
// fact is stated as a build constraint. Splitting only the LEG rather than
// constraining the whole file keeps the rest of each vertical — the DataDir
// placement, the cross-tenant isolation, the encryption-at-rest scan — running
// in the untagged build, where it is real coverage that a file-level constraint
// would have deleted.
//
// The symbol is declared in every build, which is what the shared-helper rule
// requires of a helper called from three unconstrained files: the constraint
// selects the BODY, never the existence of the name.
func assertTimeSeriesLeg(ctx context.Context, t *testing.T, ts service.TimeSeriesServicer, label, metric string) {
	t.Helper()

	at := time.Now()
	if err := ts.Write(ctx, metric, 0.5, at, map[string]string{"h": "1"}); err != nil {
		t.Fatalf("%sTimeSeries.Write: %v", label, err)
	}
	pts, err := ts.Query(ctx, metric, at.Add(-time.Minute), at.Add(time.Minute))
	if err != nil {
		t.Fatalf("%sTimeSeries.Query: %v", label, err)
	}
	if len(pts) == 0 {
		t.Fatalf("%sTimeSeries.Query returned no points; want >=1", label)
	}
}

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

package sdk_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// fakeProbe is a deterministic sdk.SealedWindowProbe.
type fakeProbe struct {
	sealed bool
}

func (p *fakeProbe) IsSealed() bool { return p.sealed }

// Compile-time checks that the sealed-window re-exports satisfy the
// upstream contracts.
var (
	_ sdk.SealedWindowProbe = (*fakeProbe)(nil)
	_ sdk.ServerOption      = sdk.WithSealedWindowProbe(&fakeProbe{})
	_ sdk.ServerOption      = sdk.WithAuthMiddleware(nil)
)

// TestSealedWindowAllowlists_ReExported verifies the four per-transport
// allowlists are populated and agree on representative verbs, proving
// they derive from the same canonical table.
func TestSealedWindowAllowlists_ReExported(t *testing.T) {
	if len(sdk.RESTSealedWindowAllowlist) == 0 {
		t.Fatal("REST allowlist re-export is empty")
	}
	if len(sdk.GRPCSealedWindowAllowlist) == 0 {
		t.Fatal("gRPC allowlist re-export is empty")
	}
	if len(sdk.QUICSealedWindowAllowlist) == 0 {
		t.Fatal("QUIC allowlist re-export is empty")
	}
	if len(sdk.MCPSealedWindowAllowlist) == 0 {
		t.Fatal("MCP allowlist re-export is empty")
	}

	restHas := func(p string) bool {
		for _, e := range sdk.RESTSealedWindowAllowlist {
			if e == p {
				return true
			}
		}
		return false
	}
	grpcHas := func(m string) bool {
		for _, e := range sdk.GRPCSealedWindowAllowlist {
			if e == m {
				return true
			}
		}
		return false
	}

	// The unified rekey verb must be present on all four transports. The gRPC
	// allowlist is fully service-qualified (a security hardening: matching the
	// bare method tail would admit a same-named method on any co-registered
	// service while sealed), so the entry is the full /service/method path.
	if !restHas("/v1/barrier/rekey") {
		t.Error("REST allowlist missing /v1/barrier/rekey")
	}
	if !grpcHas("/qrdb.v1.QRDBService/BarrierRekey") {
		t.Error("gRPC allowlist missing /qrdb.v1.QRDBService/BarrierRekey")
	}
	if _, ok := sdk.QUICSealedWindowAllowlist["barrier.Rekey"]; !ok {
		t.Error("QUIC allowlist missing barrier.Rekey")
	}
	if _, ok := sdk.MCPSealedWindowAllowlist["qrdb_barrier_rekey"]; !ok {
		t.Error("MCP allowlist missing qrdb_barrier_rekey")
	}

	// SealedWindowRESTAllowlist derives a fresh, equal list.
	fresh := sdk.SealedWindowRESTAllowlist()
	if len(fresh) != len(sdk.RESTSealedWindowAllowlist) {
		t.Errorf("fresh REST derivation has %d entries, shared var has %d",
			len(fresh), len(sdk.RESTSealedWindowAllowlist))
	}
}

// TestSealedWindowMiddleware_ReExport verifies the re-exported REST
// middleware enforces the sealed envelope with a probe supplied through
// the SDK types.
func TestSealedWindowMiddleware_ReExport(t *testing.T) {
	mw := sdk.SealedWindowMiddleware(&fakeProbe{sealed: true}, nil)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Non-allowlisted path → sealed envelope.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/kv/foo", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"SEALED"`) {
		t.Errorf("body = %q, want SEALED envelope", rec.Body.String())
	}

	// Allowlisted path → passthrough.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/barrier/unseal", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("allowlisted path status = %d, want 200", rec.Code)
	}
}

// TestBuildSealServicesOnly_ReExport verifies the lifecycle re-export is
// callable and enforces its nil-barrier contract through the SDK alias.
func TestBuildSealServicesOnly_ReExport(t *testing.T) {
	if _, err := sdk.BuildSealServicesOnly(t.Context(), sdk.LifecycleInfra{}); err == nil {
		t.Fatal("BuildSealServicesOnly must reject a nil seal barrier")
	}
}

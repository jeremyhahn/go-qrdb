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
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// fakeProbe is a deterministic sdk.SealedWindowProbe.
type fakeProbe struct {
	sealed, enrollment bool
	sealedwindow.Admission
}

func (p *fakeProbe) IsSealed() bool                      { return p.sealed }
func (p *fakeProbe) EnrollmentOpen(context.Context) bool { return p.enrollment }

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

	// The unified unseal verb must be present on all four transports. The
	// gRPC allowlist is fully service-qualified (a security hardening:
	// matching the bare method tail would admit a same-named method on any
	// co-registered service while sealed), so the entry is the full
	// /service/method path.
	if !restHas("/v1/barrier/unseal") {
		t.Error("REST allowlist missing /v1/barrier/unseal")
	}
	if !grpcHas("/qrdb.v1.QRDBService/BarrierUnseal") {
		t.Error("gRPC allowlist missing /qrdb.v1.QRDBService/BarrierUnseal")
	}
	if _, ok := sdk.QUICSealedWindowAllowlist["barrier.Unseal"]; !ok {
		t.Error("QUIC allowlist missing barrier.Unseal")
	}
	if _, ok := sdk.MCPSealedWindowAllowlist["qrdb_barrier_unseal"]; !ok {
		t.Error("MCP allowlist missing qrdb_barrier_unseal")
	}

	// Rekey refuses while sealed, so it is on no transport's allowlist: on
	// the boot-time listener it would be an anonymous destructive verb.
	if restHas("/v1/barrier/rekey") || grpcHas("/qrdb.v1.QRDBService/BarrierRekey") {
		t.Error("rekey must not be on the sealed-window allowlist")
	}

	// The share-holder quorum submission — how a sealed barrier is unsealed —
	// must be present on all four transports. The gRPC allowlist is fully
	// service-qualified (a security hardening: matching the bare method tail
	// would admit a same-named method on any co-registered service while
	// sealed), so the entry is the full /service/method path.
	if !restHas("/v1/barrier/quorum/submit") {
		t.Error("REST allowlist missing /v1/barrier/quorum/submit")
	}
	if !grpcHas("/qrdb.v1.QRDBService/BarrierQuorumSubmit") {
		t.Error("gRPC allowlist missing /qrdb.v1.QRDBService/BarrierQuorumSubmit")
	}
	if _, ok := sdk.QUICSealedWindowAllowlist["barrier.QuorumSubmit"]; !ok {
		t.Error("QUIC allowlist missing barrier.QuorumSubmit")
	}
	if _, ok := sdk.MCPSealedWindowAllowlist["qrdb_barrier_quorum_submit"]; !ok {
		t.Error("MCP allowlist missing qrdb_barrier_quorum_submit")
	}

	// Opening a rekey needs an unsealed barrier: it must NOT be admitted while
	// sealed on any transport.
	if grpcHas("/qrdb.v1.QRDBService/BarrierRekeyStart") {
		t.Error("gRPC allowlist admits BarrierRekeyStart while sealed")
	}
	if _, ok := sdk.QUICSealedWindowAllowlist["barrier.RekeyStart"]; ok {
		t.Error("QUIC allowlist admits barrier.RekeyStart while sealed")
	}
	if _, ok := sdk.MCPSealedWindowAllowlist["qrdb_barrier_rekey_start"]; ok {
		t.Error("MCP allowlist admits qrdb_barrier_rekey_start while sealed")
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

// TestSealedWindowListenerMiddleware_ReExport verifies the re-exported
// boot-time listener gate refuses an allowlisted verb once the barrier
// probe reports unsealed.
func TestSealedWindowListenerMiddleware_ReExport(t *testing.T) {
	probe := &fakeProbe{sealed: true}
	handler := sdk.SealedWindowListenerMiddleware(probe, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/barrier/unseal", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("allowlisted path while sealed: status = %d, want 200", rec.Code)
	}
	probe.sealed = false
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/barrier/unseal", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("allowlisted path once unsealed: status = %d, want 503", rec.Code)
	}
	if sdk.SealedWindowListenerUnaryInterceptor(probe, nil) == nil || sdk.SealedWindowListenerStreamInterceptor(probe, nil) == nil {
		t.Fatal("listener interceptor re-exports returned nil")
	}
}

// TestBuildSealServicesOnly_ReExport verifies the lifecycle re-export is
// callable and enforces its nil-barrier contract through the SDK alias.
func TestBuildSealServicesOnly_ReExport(t *testing.T) {
	if _, err := sdk.BuildSealServicesOnly(t.Context(), sdk.LifecycleInfra{}); err == nil {
		t.Fatal("BuildSealServicesOnly must reject a nil seal barrier")
	}
}

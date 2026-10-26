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

package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	sdkserver "github.com/jeremyhahn/go-qrdb/sdk/go/server"
)

// fakeProbe is a deterministic sdkserver.SealedWindowProbe.
type fakeProbe struct {
	sealed, enrollment bool
	sealedwindow.Admission
}

func (p *fakeProbe) IsSealed() bool                      { return p.sealed }
func (p *fakeProbe) EnrollmentOpen(context.Context) bool { return p.enrollment }

// Compile-time checks that the sealed-window re-exports satisfy the
// upstream contracts.
var (
	_ sdkserver.SealedWindowProbe = (*fakeProbe)(nil)
	_ sdkserver.ServerOption      = sdkserver.WithSealedWindowProbe(&fakeProbe{})
	_ sdkserver.ServerOption      = sdkserver.WithAuthMiddleware(nil)
)

// TestSealedWindowAllowlists_ReExported verifies the four per-transport
// allowlists are populated and agree on representative verbs, proving
// they derive from the same canonical table.
func TestSealedWindowAllowlists_ReExported(t *testing.T) {
	if len(sdkserver.RESTSealedWindowAllowlist) == 0 {
		t.Fatal("REST allowlist re-export is empty")
	}
	if len(sdkserver.GRPCSealedWindowAllowlist) == 0 {
		t.Fatal("gRPC allowlist re-export is empty")
	}
	if len(sdkserver.QUICSealedWindowAllowlist) == 0 {
		t.Fatal("QUIC allowlist re-export is empty")
	}
	if len(sdkserver.MCPSealedWindowAllowlist) == 0 {
		t.Fatal("MCP allowlist re-export is empty")
	}

	restHas := func(p string) bool {
		for _, e := range sdkserver.RESTSealedWindowAllowlist {
			if e == p {
				return true
			}
		}
		return false
	}
	grpcHas := func(m string) bool {
		for _, e := range sdkserver.GRPCSealedWindowAllowlist {
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
	if _, ok := sdkserver.QUICSealedWindowAllowlist["barrier.Unseal"]; !ok {
		t.Error("QUIC allowlist missing barrier.Unseal")
	}
	if _, ok := sdkserver.MCPSealedWindowAllowlist["qrdb_barrier_unseal"]; !ok {
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
	if _, ok := sdkserver.QUICSealedWindowAllowlist["barrier.QuorumSubmit"]; !ok {
		t.Error("QUIC allowlist missing barrier.QuorumSubmit")
	}
	if _, ok := sdkserver.MCPSealedWindowAllowlist["qrdb_barrier_quorum_submit"]; !ok {
		t.Error("MCP allowlist missing qrdb_barrier_quorum_submit")
	}

	// Opening a rekey needs an unsealed barrier: it must NOT be admitted while
	// sealed on any transport.
	if grpcHas("/qrdb.v1.QRDBService/BarrierRekeyStart") {
		t.Error("gRPC allowlist admits BarrierRekeyStart while sealed")
	}
	if _, ok := sdkserver.QUICSealedWindowAllowlist["barrier.RekeyStart"]; ok {
		t.Error("QUIC allowlist admits barrier.RekeyStart while sealed")
	}
	if _, ok := sdkserver.MCPSealedWindowAllowlist["qrdb_barrier_rekey_start"]; ok {
		t.Error("MCP allowlist admits qrdb_barrier_rekey_start while sealed")
	}

	// SealedWindowRESTAllowlist derives a fresh, equal list.
	fresh := sdkserver.SealedWindowRESTAllowlist()
	if len(fresh) != len(sdkserver.RESTSealedWindowAllowlist) {
		t.Errorf("fresh REST derivation has %d entries, shared var has %d",
			len(fresh), len(sdkserver.RESTSealedWindowAllowlist))
	}
}

// TestSealedWindowRESTEntryMatches_ReExport checks that the alias is the
// gate's matcher: every allowlist entry admits a concrete request built from
// its own pattern, and a path on no entry is admitted by none.
func TestSealedWindowRESTEntryMatches_ReExport(t *testing.T) {
	// concrete turns an entry into a method and a path the entry admits.
	concrete := func(entry string) (string, string) {
		method, path := http.MethodGet, entry
		if m, p, qualified := strings.Cut(entry, " "); qualified {
			method, path = m, p
		}
		if strings.HasSuffix(path, "/") {
			path += "probe"
		}
		segs := strings.Split(path, "/")
		for i, s := range segs {
			if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
				segs[i] = "probe"
			}
		}
		return method, strings.Join(segs, "/")
	}

	entries := sdkserver.SealedWindowRESTAllowlist()
	if len(entries) == 0 {
		t.Fatal("REST allowlist is empty")
	}
	for _, entry := range entries {
		method, path := concrete(entry)
		if !sdkserver.SealedWindowRESTEntryMatches(entry, method, path) {
			t.Errorf("SealedWindowRESTEntryMatches(%q, %q, %q) = false, want true", entry, method, path)
		}
		if got, want := sdkserver.SealedWindowRESTEntryMatches(entry, method, path),
			sealedwindow.RESTEntryMatches(entry, method, path); got != want {
			t.Errorf("alias and sealedwindow.RESTEntryMatches disagree on %q %q %q", entry, method, path)
		}
	}

	// A data-plane path is on no entry while sealed.
	for _, entry := range entries {
		if sdkserver.SealedWindowRESTEntryMatches(entry, http.MethodGet, "/v1/kv/probe") {
			t.Errorf("entry %q admits GET /v1/kv/probe", entry)
		}
	}
}

// TestSealedWindowMiddleware_ReExport verifies the re-exported REST
// middleware enforces the sealed envelope with a probe supplied through
// the SDK types.
func TestSealedWindowMiddleware_ReExport(t *testing.T) {
	mw := sdkserver.SealedWindowMiddleware(&fakeProbe{sealed: true}, nil)
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
	handler := sdkserver.SealedWindowListenerMiddleware(probe, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	if sdkserver.SealedWindowListenerUnaryInterceptor(probe, nil) == nil || sdkserver.SealedWindowListenerStreamInterceptor(probe, nil) == nil {
		t.Fatal("listener interceptor re-exports returned nil")
	}
}

// TestBuildSealServicesOnly_ReExport verifies the lifecycle re-export is
// callable and enforces its nil-barrier contract through the SDK alias.
func TestBuildSealServicesOnly_ReExport(t *testing.T) {
	if _, err := sdkserver.BuildSealServicesOnly(t.Context(), sdkserver.LifecycleInfra{}); err == nil {
		t.Fatal("BuildSealServicesOnly must reject a nil seal barrier")
	}
}
